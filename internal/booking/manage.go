package booking

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// Managed is what a management link shows: never the visitor's details or any note.
type Managed struct {
	Reference               string
	Status                  Status
	ServiceSlug             string
	ServiceName             string
	StartsAt, EndsAt        time.Time
	DurationMinutes         int
	Timezone, Format        string
	CanCancel               bool
	CanRequestReschedule    bool
	CancellationNoticeHours int
	LateIfCancelledNow      bool
	RescheduleRequested     bool
}

var (
	// The same for an unknown and an expired link, so a link never reveals whether an appointment exists.
	errLinkNotFound   = apperr.New(apperr.NotFound, "This link is not valid or has expired.")
	errLinkNotAllowed = apperr.New(apperr.ActionNotAllowed,
		"This appointment can no longer be changed through its link.")
)

func GetManaged(ctx context.Context, q db.Querier, token string, now time.Time) (Managed, error) {
	row, err := q.GetManagedAppointment(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Managed{}, errLinkNotFound
	}
	if err != nil {
		return Managed{}, err
	}
	status := Status(row.Status)
	if linkExpired(status, row.EndsAt, now) {
		return Managed{}, errLinkNotFound
	}
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Managed{}, err
	}
	requested, err := q.RescheduleRequestOpen(ctx, row.ID)
	if err != nil {
		return Managed{}, err
	}
	notice := time.Duration(cur.CancellationNoticeHours) * time.Hour
	changeable := actionable(status, row.StartsAt, now)
	return Managed{
		Reference:               row.Reference,
		Status:                  status,
		ServiceSlug:             row.ServiceSlug,
		ServiceName:             inLocale(row.ServiceName, row.Locale),
		StartsAt:                row.StartsAt.UTC(),
		EndsAt:                  row.EndsAt.UTC(),
		DurationMinutes:         int(row.DurationMinutes),
		Timezone:                row.Timezone,
		Format:                  row.Format,
		CanCancel:               changeable,
		CanRequestReschedule:    changeable,
		CancellationNoticeHours: cur.CancellationNoticeHours,
		LateIfCancelledNow:      changeable && late(row.StartsAt, now, notice),
		RescheduleRequested:     requested,
	}, nil
}

// CancelByClient cancels through a management link, recording it as late inside the cancellation notice.
func CancelByClient(ctx context.Context, pool *pgxpool.Pool, token, message string, now time.Time) (Changed, error) {
	var out Changed
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		appt, err := lockByToken(ctx, q, token, now)
		if err != nil {
			return err
		}
		from := Status(appt.Status)
		if !CanTransition(from, CancelledByClient) {
			return errLinkNotAllowed
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		isLate := late(appt.StartsAt, now, time.Duration(cur.CancellationNoticeHours)*time.Hour)
		if _, err := q.CancelAppointmentByClient(ctx, db.CancelAppointmentByClientParams{
			ID: appt.ID, LateCancellation: isLate, Now: now,
		}); err != nil {
			return err
		}
		if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "cancelled", From: from,
			To: CancelledByClient, Actor: "visitor",
			Detail: EventDetail{By: "client", LateCancellation: &isLate}}); err != nil {
			return err
		}
		if err := CancelReminders(ctx, q, appt.ID, string(CancelledByClient)); err != nil {
			return err
		}
		if out.EndedRoom, err = video.EndRoom(ctx, q, appt.ID, video.EndedByCancellation, now); err != nil {
			return err
		}
		if from == Pending {
			out.Remove = []queue.Task{holdTask(appt.ID, time.Time{})}
		}
		out.Tasks, err = queueClientCancelled(ctx, q, appt, cur, message)
		return err
	})
	return out, err
}

func queueClientCancelled(ctx context.Context, q db.Querier, appt db.Appointment, cur settings.Settings,
	message string) ([]queue.Task, error) {
	visitor, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled,
		Recipient: appt.VisitorEmail, Locale: appt.Locale})
	if err != nil {
		return nil, err
	}
	practitioner, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
		Kind: comms.PractitionerClientCancelled, Recipient: cur.ContactEmail, Text: strings.TrimSpace(message)})
	return []queue.Task{visitor, practitioner}, err
}

// RequestReschedule only tells Daw Mi; the appointment keeps its time until she moves it.
func RequestReschedule(ctx context.Context, pool *pgxpool.Pool, token string, preferred []time.Time, message string,
	now time.Time) (Changed, error) {
	var out Changed
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		appt, err := lockByToken(ctx, q, token, now)
		if err != nil {
			return err
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		utc := make([]time.Time, len(preferred))
		for i, p := range preferred {
			utc[i] = p.UTC()
		}
		if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "reschedule_requested", Actor: "visitor",
			Detail: EventDetail{Preferred: utc}}); err != nil {
			return err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
			Kind: comms.PractitionerRescheduleRequested, Recipient: cur.ContactEmail, Text: strings.TrimSpace(message)})
		out.Tasks = []queue.Task{task}
		return err
	})
	return out, err
}

func lockByToken(ctx context.Context, q db.Querier, token string, now time.Time) (db.Appointment, error) {
	appt, err := q.LockAppointmentByTokenHash(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Appointment{}, errLinkNotFound
	}
	if err != nil {
		return db.Appointment{}, err
	}
	status := Status(appt.Status)
	if linkExpired(status, appt.EndsAt, now) {
		return db.Appointment{}, errLinkNotFound
	}
	if !actionable(status, appt.StartsAt, now) {
		return db.Appointment{}, errLinkNotAllowed
	}
	return appt, nil
}

func linkExpired(s Status, endsAt, now time.Time) bool {
	return Terminal(s) && endsAt.Before(now)
}

func actionable(s Status, startsAt, now time.Time) bool {
	return (s == Pending || s == Confirmed) && startsAt.After(now)
}

// late compares real hours, so across a daylight-saving change the wall-clock gap may differ.
func late(startsAt, now time.Time, notice time.Duration) bool {
	return startsAt.Sub(now) < notice
}
