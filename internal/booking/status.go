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

var (
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
	errStaleAppointment    = apperr.New(apperr.StaleVersion, "The appointment changed since it was read; reload it.")
)

// CanTransition guards every status change.
func CanTransition(from, to Status) bool {
	return slices.Contains(transitions[from], to)
}

func Terminal(status Status) bool {
	return len(transitions[status]) == 0
}

// Confirm keeps the row inside appointments_no_overlap, so its time is never free in between.
func Confirm(ctx context.Context, pool *pgxpool.Pool, secret []byte, change Change, now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Changed{}, err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return Changed{}, err
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Changed{}, err
	}
	if !appointment.StartsAt.After(now) {
		return Changed{}, invalidTransition(Status(appointment.Status), "confirmed")
	}
	appointment, err = setStatus(ctx, q, appointment, Confirmed, "confirmed", change, EventDetail{}, now)
	if err != nil {
		return Changed{}, err
	}
	tasks, err := afterConfirm(ctx, q, secret, appointment, current, now, true)
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	changed := Changed{
		Tasks:  tasks,
		Remove: []queue.Task{holdTask(appointment.ID, time.Time{})},
	}
	return changed, nil
}

func Decline(ctx context.Context, pool *pgxpool.Pool, change Change, now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Changed{}, err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return Changed{}, err
	}
	appointment, err = setStatus(ctx, q, appointment, Declined, "declined", change, EventDetail{}, now)
	if err != nil {
		return Changed{}, err
	}
	message := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.RequestDeclined,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
		Text:          change.ToVisitor,
	}
	task, err := comms.Queue(ctx, q, message)
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	changed := Changed{
		Tasks:  []queue.Task{task},
		Remove: []queue.Task{holdTask(appointment.ID, time.Time{})},
	}
	return changed, nil
}

func Cancel(ctx context.Context, pool *pgxpool.Pool, change Change, now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Changed{}, err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return Changed{}, err
	}
	if !appointment.StartsAt.After(now) {
		return Changed{}, invalidTransition(Status(appointment.Status), "cancelled")
	}
	detail := EventDetail{By: "practitioner"}
	appointment, err = setStatus(ctx, q, appointment, CancelledByPractitioner, "cancelled", change, detail, now)
	if err != nil {
		return Changed{}, err
	}
	err = CancelReminders(ctx, q, appointment.ID, string(CancelledByPractitioner))
	if err != nil {
		return Changed{}, err
	}
	room, err := video.EndRoom(ctx, q, appointment.ID, video.EndedByCancellation, now)
	if err != nil {
		return Changed{}, err
	}
	changed := Changed{EndedRoom: room}
	if change.Notify {
		message := comms.Message{
			AppointmentID: appointment.ID,
			Kind:          comms.Cancelled,
			Recipient:     appointment.VisitorEmail,
			Locale:        appointment.Locale,
			Text:          change.ToVisitor,
		}
		task, err := comms.Queue(ctx, q, message)
		if err != nil {
			return Changed{}, err
		}
		changed.Tasks = []queue.Task{task}
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	return changed, nil
}

func Complete(ctx context.Context, pool *pgxpool.Pool, change Change, now time.Time) (Changed, error) {
	return recordOutcome(ctx, pool, change, Completed, "completed", now)
}

func MarkNoShow(ctx context.Context, pool *pgxpool.Pool, change Change, now time.Time) (Changed, error) {
	return recordOutcome(ctx, pool, change, NoShow, "no_show", now)
}

// recordOutcome is for an appointment whose start has passed.
func recordOutcome(ctx context.Context, pool *pgxpool.Pool, change Change, to Status, kind string,
	now time.Time) (Changed, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Changed{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Changed{}, err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return Changed{}, err
	}
	if appointment.StartsAt.After(now) {
		return Changed{}, invalidTransition(Status(appointment.Status), kind)
	}
	_, err = setStatus(ctx, q, appointment, to, kind, change, EventDetail{}, now)
	if err != nil {
		return Changed{}, err
	}
	err = CancelReminders(ctx, q, appointment.ID, string(to))
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	return Changed{}, nil
}

// lockAppointment refuses the change when the admin's version is stale.
func lockAppointment(ctx context.Context, q db.Querier, id pgtype.UUID, version int32) (db.Appointment, error) {
	appointment, err := q.LockAppointment(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Appointment{}, errAppointmentNotFound
	}
	if err != nil {
		return db.Appointment{}, err
	}
	if appointment.Version != version {
		return db.Appointment{}, errStaleAppointment
	}
	return appointment, nil
}

func setStatus(ctx context.Context, q db.Querier, appointment db.Appointment, to Status, kind string, change Change,
	detail EventDetail, now time.Time) (db.Appointment, error) {
	from := Status(appointment.Status)
	if !CanTransition(from, to) {
		return db.Appointment{}, invalidTransition(from, kind)
	}
	params := db.SetAppointmentStatusParams{
		ID:     appointment.ID,
		Status: string(to),
		Now:    now,
	}
	updated, err := q.SetAppointmentStatus(ctx, params)
	if err != nil {
		return db.Appointment{}, err
	}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          kind,
		From:          from,
		To:            to,
		Actor:         "admin",
		ActorUserID:   change.Actor,
		Detail:        detail,
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return db.Appointment{}, err
	}
	return updated, nil
}

func invalidTransition(from Status, action string) error {
	return apperr.New(apperr.InvalidTransition, "A "+string(from)+" appointment cannot be "+action+" now.")
}
