package comms

import (
	"context"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

const (
	sweepGrace = time.Minute // leaves a fresh row to the task its writer enqueued
	sweepLimit = 500
)

// DueTasks re-creates tasks lost between commit and enqueue. Task ids match
// Queue's, so a row whose task still exists is not enqueued twice.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]queue.Task, error) {
	rows, err := q.ListDueCommunications(ctx, db.ListDueCommunicationsParams{
		Before: now.Add(-sweepGrace), MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}
	tasks := make([]queue.Task, len(rows))
	for i, r := range rows {
		tasks[i] = deliverTask(r.ID, Kind(r.Kind), r.ScheduledFor)
	}
	return tasks, nil
}

func (t *Tasks) Sweep(ctx context.Context, _ []byte) error {
	tasks, err := DueTasks(ctx, db.New(t.Pool), t.Now())
	if err != nil {
		return err
	}
	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "communications swept", "found", len(tasks))
	return nil
}

// RescheduleReminderRows moves queued reminders to the current reminder_hours.
func RescheduleReminderRows(ctx context.Context, q db.Querier) ([]queue.Task, error) {
	s, err := settings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.RescheduleQueuedReminders(ctx, int32(s.ReminderHours))
	if err != nil {
		return nil, err
	}
	tasks := make([]queue.Task, len(rows))
	for i, r := range rows {
		tasks[i] = deliverTask(r.ID, Reminder, r.ScheduledFor)
	}
	return tasks, nil
}

func (t *Tasks) RescheduleReminders(ctx context.Context, _ []byte) error {
	tasks, err := RescheduleReminderRows(ctx, db.New(t.Pool))
	if err != nil {
		return err
	}
	// Replace, not Enqueue: asynq would otherwise keep each task's old time.
	t.Queue.Replace(ctx, tasks...)
	t.Log.InfoContext(ctx, "reminders rescheduled", "count", len(tasks))
	return nil
}
