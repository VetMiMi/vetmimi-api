package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

// ScheduleReminder queues the visitor's reminder for a confirmed appointment
// at its start less reminderHours, in the caller's transaction, and returns
// the task to enqueue after commit. Hours are real hours, so across a
// daylight-saving change the wall-clock time shifts. When that instant has
// already passed, as for an appointment confirmed two hours before it starts,
// there is no reminder and no task.
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

// CancelReminders cancels every queued reminder of an appointment, recording
// reason (a short code such as comms.SkipSuperseded). Reschedule, cancel,
// decline, expiry and completion call it; the reminder would be skipped at
// fire time anyway, but cancelling it keeps the history truthful.
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
