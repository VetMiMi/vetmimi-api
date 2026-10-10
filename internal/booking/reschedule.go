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

// Reschedule moves an appointment with one UPDATE, so if the new time cannot be had, the old one is kept.
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
		moved, err := move(ctx, q, appt, svc, Period{Start: start, End: start.Add(duration)}, c, now)
		if err != nil {
			return Changed{}, err
		}
		return rescheduled(ctx, q, moved, cur, c.Notify, now)
	})
	return out, countConflict("reschedule", err)
}

func move(ctx context.Context, q *db.Queries, appt db.Appointment, svc db.Service, to Period, c Change,
	now time.Time) (db.Appointment, error) {
	moved, err := q.MoveAppointment(ctx, db.MoveAppointmentParams{
		ID: appt.ID, StartsAt: to.Start, EndsAt: to.End,
		BusyRange: BusyRange(to.Start, to.End.Sub(to.Start), svc).tstzrange(), Now: now,
	})
	if err != nil {
		return db.Appointment{}, withAlternatives(refusal(err))
	}
	previous := Period{Start: appt.StartsAt, End: appt.EndsAt}
	next := Period{Start: moved.StartsAt, End: moved.EndsAt}
	return moved, AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "rescheduled", Previous: &previous,
		New: &next, Actor: "admin", ActorUserID: c.Actor})
}

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
	if !notify {
		return out, nil
	}
	task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Rescheduled,
		Recipient: appt.VisitorEmail, Locale: appt.Locale})
	if err != nil {
		return Changed{}, err
	}
	out.Tasks = append(out.Tasks, task)
	return out, nil
}
