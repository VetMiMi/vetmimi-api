package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

var errPastStart = apperr.New(apperr.OutsideBookingWindow, "The new start has already passed.")

// Reschedule moves a pending or confirmed appointment that has not started
// to start, keeping its status and its length (ADR-004). The new time must
// be a free slot, its own old time counting as free; Daw Mi may move it
// inside the minimum notice or beyond the furthest bookable day, but never
// onto other busy time. One UPDATE takes the new time and releases the old,
// so if the new time cannot be had, nothing changes. A confirmed
// appointment's reminder follows it, and unless Daw Mi tells the visitor
// herself, they are emailed both times.
func Reschedule(ctx context.Context, pool *pgxpool.Pool, c Change, start, now time.Time) (Changed, error) {
	out, err := change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, cur settings.Settings) (Changed, error) {
		status := Status(appt.Status)
		if (status != Pending && status != Confirmed) || !appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(status, "rescheduled")
		}
		if !start.After(now) {
			return Changed{}, errPastStart
		}
		svc, err := q.GetService(ctx, appt.ServiceID)
		if err != nil {
			return Changed{}, err
		}
		duration := time.Duration(appt.DurationMinutes) * time.Minute
		if err := checkSlot(ctx, q, cur, slotRequest{Service: svc, Duration: duration, Start: start, Moving: appt.ID},
			now); err != nil {
			return Changed{}, err
		}

		previous := Period{Start: appt.StartsAt, End: appt.EndsAt}
		moved, err := q.MoveAppointment(ctx, db.MoveAppointmentParams{
			ID: appt.ID, StartsAt: start, EndsAt: start.Add(duration),
			BusyRange: BusyRange(start, duration, svc).tstzrange(), Now: now,
		})
		if err != nil {
			return Changed{}, withAlternatives(refusal(err))
		}
		next := Period{Start: moved.StartsAt, End: moved.EndsAt}
		if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "rescheduled", Previous: &previous,
			New: &next, Actor: "admin", ActorUserID: c.Actor}); err != nil {
			return Changed{}, err
		}
		return rescheduled(ctx, q, moved, cur, c.Notify, now)
	})
	return out, countConflict("reschedule", err)
}

// rescheduled queues what a move sends: a confirmed appointment's reminder
// for its new time and its video room's new window, and the visitor's email.
// A pending request's hold may have moved earlier, so its expiry task is
// replaced.
func rescheduled(ctx context.Context, q db.Querier, appt db.Appointment, cur settings.Settings, notify bool,
	now time.Time) (Changed, error) {
	var out Changed
	if Status(appt.Status) == Pending {
		out.Replace = []queue.Task{holdTask(appt.ID, appt.HoldExpiresAt.Time)}
	} else {
		if err := CancelReminders(ctx, q, appt.ID, comms.SkipSuperseded); err != nil {
			return Changed{}, err
		}
		reminder, err := ScheduleReminder(ctx, q, appt, cur.ReminderHours, now)
		if err != nil {
			return Changed{}, err
		}
		room, err := video.MoveRoom(ctx, q, appt, now)
		if err != nil {
			return Changed{}, err
		}
		out.Tasks = append(reminder, room...)
	}
	if notify {
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Rescheduled,
			Recipient: appt.VisitorEmail, Locale: appt.Locale})
		if err != nil {
			return Changed{}, err
		}
		out.Tasks = append(out.Tasks, task)
	}
	return out, nil
}
