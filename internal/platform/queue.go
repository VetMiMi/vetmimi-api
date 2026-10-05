package platform

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// The worker takes about six critical tasks for every three default and one
// low (see Worker).
const (
	QueueCritical = "critical"
	QueueDefault  = "default"
	QueueLow      = "low"
)

// Task is background work for the worker.
type Task struct {
	Type string
	// Payload is sent as JSON and carries ids only. The handler loads the
	// current row from PostgreSQL, so a task never acts on stale data and
	// no personal data sits in Redis.
	Payload any
	// ID, when set, makes enqueueing idempotent: a task whose id is already
	// in its queue is not added again.
	ID string
	// ProcessAt delays the task; zero runs it now.
	ProcessAt time.Time
	// Queue is QueueCritical, QueueDefault or QueueLow; empty means
	// QueueDefault.
	Queue string
}

// Queue adds tasks to the asynq queues in Redis.
type Queue struct {
	client *asynq.Client
	log    *slog.Logger
	// ns prefixes every queue name. Tests set it so that test binaries
	// sharing one Redis never take each other's tasks; it is empty otherwise.
	ns string
}

// NewQueue returns a Queue that shares rdb's connections.
func NewQueue(rdb *redis.Client, log *slog.Logger) *Queue {
	return &Queue{client: asynq.NewClientFromRedisClient(rdb), log: log}
}

// Enqueue adds tasks and reports nothing back. The transaction that produced
// them has committed, and a sweeper rebuilds any lost task from PostgreSQL
// (ADR-006), so a Redis failure is logged and must not fail a request that
// has already succeeded. A task whose id is already queued counts as added.
func (q *Queue) Enqueue(ctx context.Context, tasks ...Task) {
	for _, t := range tasks {
		err := q.enqueue(ctx, t)
		if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
			q.log.Error("enqueue_failed", "task", t.Type, "task_id", t.ID, "err", err)
		}
	}
}

func (q *Queue) enqueue(ctx context.Context, t Task) error {
	payload, err := json.Marshal(t.Payload)
	if err != nil {
		return err
	}
	opts := []asynq.Option{asynq.Queue(q.ns + cmp.Or(t.Queue, QueueDefault))}
	if t.ID != "" {
		opts = append(opts, asynq.TaskID(t.ID))
	}
	if !t.ProcessAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(t.ProcessAt))
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(t.Type, payload), opts...)
	return err
}
