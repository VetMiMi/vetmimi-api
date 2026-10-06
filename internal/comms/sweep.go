package comms

import (
	"context"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

const (
	// sweepGrace leaves a fresh row to the task its writer enqueued.
	sweepGrace = time.Minute
	// sweepLimit bounds one run; the next takes the rest.
	sweepLimit = 500
)

// DueTasks rebuilds the delivery task of every queued row due more than a
// minute before now, oldest first (ADR-006: a crash between commit and
// enqueue, or a lost Redis, leaves rows with no task). The task ids are the
// ones Queue gives, so a row whose task still exists is not enqueued twice.
// Future reminders are left to their own tasks.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]platform.Task, error) {
	rows, err := q.ListDueCommunications(ctx, db.ListDueCommunicationsParams{
		Before: now.Add(-sweepGrace), MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}
	tasks := make([]platform.Task, len(rows))
	for i, r := range rows {
		tasks[i] = deliverTask(r.ID, Kind(r.Kind), r.ScheduledFor)
	}
	return tasks, nil
}

// Sweep enqueues DueTasks. It changes no row; delivery does.
func (t *Tasks) Sweep(ctx context.Context, _ []byte) error {
	tasks, err := DueTasks(ctx, db.New(t.Pool), t.Now())
	if err != nil {
		return err
	}
	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "communications swept", "found", len(tasks))
	return nil
}
