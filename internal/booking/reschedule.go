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
func Reschedule(ctx context.Context, pool *pgxpool.Pool, change Change, start, now time.Time) (Changed, error) {
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
	status := Status(appointment.Status)
	if (status != Pending && status != Confirmed) || !appointment.StartsAt.After(now) {
		return Changed{}, invalidTransition(status, "rescheduled")
	}
	if !start.After(now) {
		return Changed{}, errPastStart
	}
	service, err := q.GetService(ctx, appointment.ServiceID)
	if err != nil {
		return Changed{}, err
	}
	duration := time.Duration(appointment.DurationMinutes) * time.Minute
	request := slotRequest{
		Service:  service,
		Duration: duration,
		Start:    start,
		Moving:   appointment.ID,
	}
	err = checkSlot(ctx, q, current, request, now)
	if err != nil {
		return Changed{}, countConflict("reschedule", err)
	}
	to := Period{Start: start, End: start.Add(duration)}
	moved, err := move(ctx, q, appointment, service, to, change, now)
	if err != nil {
		return Changed{}, countConflict("reschedule", err)
	}
	changed, err := rescheduled(ctx, q, moved, current, change.Notify, now)
	if err != nil {
		return Changed{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Changed{}, err
	}
	return changed, nil
}

func move(ctx context.Context, q db.Querier, appointment db.Appointment, service db.Service, to Period, change Change,
	now time.Time) (db.Appointment, error) {
	params := db.MoveAppointmentParams{
		ID:        appointment.ID,
		StartsAt:  to.Start,
		EndsAt:    to.End,
		BusyRange: BusyRange(to.Start, to.End.Sub(to.Start), service).tstzrange(),
		Now:       now,
	}
	moved, err := q.MoveAppointment(ctx, params)
	if err != nil {
		return db.Appointment{}, withAlternatives(refusal(err))
	}
	previous := Period{Start: appointment.StartsAt, End: appointment.EndsAt}
	next := Period{Start: moved.StartsAt, End: moved.EndsAt}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "rescheduled",
		Previous:      &previous,
		New:           &next,
		Actor:         "admin",
		ActorUserID:   change.Actor,
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return db.Appointment{}, err
	}
	return moved, nil
}

// rescheduled moves a pending request's hold, or a confirmed booking's reminder and video room.
func rescheduled(ctx context.Context, q db.Querier, appointment db.Appointment, current settings.Settings,
	notify bool, now time.Time) (Changed, error) {
	var changed Changed
	if Status(appointment.Status) == Pending {
		changed.Replace = []queue.Task{holdTask(appointment.ID, appointment.HoldExpiresAt.Time)}
	} else {
		err := CancelReminders(ctx, q, appointment.ID, comms.SkipSuperseded)
		if err != nil {
			return Changed{}, err
		}
		reminder, err := ScheduleReminder(ctx, q, appointment, current.ReminderHours, now)
		if err != nil {
			return Changed{}, err
		}
		room, err := video.MoveRoom(ctx, q, appointment, now)
		if err != nil {
			return Changed{}, err
		}
		changed.Tasks = append(reminder, room...)
	}
	if !notify {
		return changed, nil
	}
	message := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.Rescheduled,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
	}
	task, err := comms.Queue(ctx, q, message)
	if err != nil {
		return Changed{}, err
	}
	changed.Tasks = append(changed.Tasks, task)
	return changed, nil
}
