package comms

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// RescheduleReminderRows moves every queued reminder to its appointment's
// start less the current reminder_hours and returns their tasks at the new
// times, so a confirmed appointment keeps exactly one reminder after Daw Mi
// changes the setting.
func RescheduleReminderRows(ctx context.Context, q db.Querier) ([]platform.Task, error) {
	s, err := settings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.RescheduleQueuedReminders(ctx, int32(s.ReminderHours))
	if err != nil {
		return nil, err
	}
	tasks := make([]platform.Task, len(rows))
	for i, r := range rows {
		tasks[i] = deliverTask(r.ID, Reminder, r.ScheduledFor)
	}
	return tasks, nil
}

// RescheduleReminders runs RescheduleReminderRows and replaces each
// reminder's task, whose old time asynq would otherwise keep.
func (t *Tasks) RescheduleReminders(ctx context.Context, _ []byte) error {
	tasks, err := RescheduleReminderRows(ctx, db.New(t.Pool))
	if err != nil {
		return err
	}
	t.Queue.Replace(ctx, tasks...)
	t.Log.InfoContext(ctx, "reminders rescheduled", "count", len(tasks))
	return nil
}
