package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

// ScheduleReminder queues no reminder when its time has already passed.
func ScheduleReminder(ctx context.Context, q db.Querier, appt db.Appointment, reminderHours int, now time.Time) ([]queue.Task, error) {
	at := appt.StartsAt.Add(-time.Duration(reminderHours) * time.Hour)
	if !at.After(now) {
		return nil, nil
	}
	task, err := comms.Queue(ctx, q, comms.Message{
		AppointmentID: appt.ID,
		Kind:          comms.Reminder,
		Recipient:     appt.VisitorEmail,
		Locale:        appt.Locale,
		ScheduledFor:  at,
	})
	if err != nil {
		return nil, err
	}
	return []queue.Task{task}, nil
}

// CancelReminders keeps the history truthful; the worker would skip the reminders anyway.
func CancelReminders(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, reason string) error {
	ids, err := q.ListQueuedReminderIDs(ctx, appointmentID)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := comms.Cancel(ctx, q, id, reason); err != nil {
			return err
		}
	}
	return nil
}
