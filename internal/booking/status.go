package booking

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

type Status string

const (
	Pending                 Status = "pending"
	Confirmed               Status = "confirmed"
	Declined                Status = "declined"
	Expired                 Status = "expired"
	CancelledByClient       Status = "cancelled_by_client"
	CancelledByPractitioner Status = "cancelled_by_practitioner"
	Completed               Status = "completed"
	NoShow                  Status = "no_show"
)

// A status with no entry is final. Pending is a request, not a booking: it completes only through confirmed.
var transitions = map[Status][]Status{
	Pending:   {Confirmed, Declined, Expired, CancelledByClient},
	Confirmed: {CancelledByClient, CancelledByPractitioner, Completed, NoShow},
}

// CanTransition guards every status change.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

func Terminal(s Status) bool {
	return len(transitions[s]) == 0
}

type Change struct {
	ID        pgtype.UUID
	Version   int32
	Actor     pgtype.UUID
	ToVisitor string
	// Notify is false when Daw Mi tells the visitor herself.
	Notify bool
}

// Changed is applied after commit; Replace swaps waiting tasks that have the same id.
type Changed struct {
	Tasks, Replace, Remove []queue.Task
	EndedRoom              pgtype.UUID
}

var (
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
	errStaleAppointment    = apperr.New(apperr.StaleVersion, "The appointment changed since it was read; reload it.")
)

func invalidTransition(from Status, action string) error {
	return apperr.New(apperr.InvalidTransition, "A "+string(from)+" appointment cannot be "+action+" now.")
}

// Confirm keeps the row inside appointments_no_overlap, so its time is never free in between.
func Confirm(ctx context.Context, pool *pgxpool.Pool, secret []byte, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, cur settings.Settings) (Changed, error) {
		if !appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), "confirmed")
		}
		appt, err := setStatus(ctx, q, appt, Confirmed, "confirmed", c, EventDetail{}, now)
		if err != nil {
			return Changed{}, err
		}
		tasks, err := afterConfirm(ctx, q, secret, appt, cur, now, true)
		return Changed{Tasks: tasks, Remove: []queue.Task{holdTask(appt.ID, time.Time{})}}, err
	})
}

func Decline(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		appt, err := setStatus(ctx, q, appt, Declined, "declined", c, EventDetail{}, now)
		if err != nil {
			return Changed{}, err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestDeclined,
			Recipient: appt.VisitorEmail, Locale: appt.Locale, Text: c.ToVisitor})
		return Changed{Tasks: []queue.Task{task}, Remove: []queue.Task{holdTask(appt.ID, time.Time{})}}, err
	})
}

func Cancel(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		if !appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), "cancelled")
		}
		appt, err := setStatus(ctx, q, appt, CancelledByPractitioner, "cancelled", c,
			EventDetail{By: "practitioner"}, now)
		if err != nil {
			return Changed{}, err
		}
		if err := CancelReminders(ctx, q, appt.ID, string(CancelledByPractitioner)); err != nil {
			return Changed{}, err
		}
		room, err := video.EndRoom(ctx, q, appt.ID, video.EndedByCancellation, now)
		if err != nil || !c.Notify {
			return Changed{EndedRoom: room}, err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled,
			Recipient: appt.VisitorEmail, Locale: appt.Locale, Text: c.ToVisitor})
		return Changed{Tasks: []queue.Task{task}, EndedRoom: room}, err
	})
}

func Complete(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return recordOutcome(ctx, pool, c, Completed, "completed", now)
}

func MarkNoShow(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return recordOutcome(ctx, pool, c, NoShow, "no_show", now)
}

func recordOutcome(ctx context.Context, pool *pgxpool.Pool, c Change, to Status, kind string, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		if appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), kind)
		}
		if _, err := setStatus(ctx, q, appt, to, kind, c, EventDetail{}, now); err != nil {
			return Changed{}, err
		}
		return Changed{}, CancelReminders(ctx, q, appt.ID, string(to))
	})
}

// change runs fn on the locked appointment, refusing it when the admin's version is stale.
func change(ctx context.Context, pool *pgxpool.Pool, c Change,
	fn func(q *db.Queries, appt db.Appointment, cur settings.Settings) (Changed, error)) (Changed, error) {
	var out Changed
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		appt, err := q.LockAppointment(ctx, c.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errAppointmentNotFound
		}
		if err != nil {
			return err
		}
		if appt.Version != c.Version {
			return errStaleAppointment
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		out, err = fn(q, appt, cur)
		return err
	})
	return out, err
}

func setStatus(ctx context.Context, q db.Querier, appt db.Appointment, to Status, kind string, c Change,
	detail EventDetail, now time.Time) (db.Appointment, error) {
	from := Status(appt.Status)
	if !CanTransition(from, to) {
		return db.Appointment{}, invalidTransition(from, kind)
	}
	updated, err := q.SetAppointmentStatus(ctx, db.SetAppointmentStatusParams{ID: appt.ID, Status: string(to), Now: now})
	if err != nil {
		return db.Appointment{}, err
	}
	return updated, AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: kind, From: from, To: to,
		Actor: "admin", ActorUserID: c.Actor, Detail: detail})
}
