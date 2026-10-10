// Package queue runs background work on asynq. Domain functions return Tasks;
// the handler enqueues them after its transaction commits, never inside it.
// The Worker runs the handlers and periodic tasks that domain packages register.
package queue

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

// The worker takes about six Critical tasks for every three Default and one Low.
const (
	Critical = "critical"
	Default  = "default"
	Low      = "low"
)

type Task struct {
	Type string
	// Payload is sent as JSON and carries ids only; the handler loads the row.
	Payload any
	// ID, when set, makes enqueueing idempotent: a task whose id is already
	// in its queue is not added again.
	ID string
	// ProcessAt delays the task; zero runs it now.
	ProcessAt time.Time
	// Queue is Critical, Default or Low; empty means Default.
	Queue string
}

// Queue adds tasks to the asynq queues in Redis.
type Queue struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	log       *slog.Logger
	ns        string // queue-name prefix, set only by tests
}

func New(rdb *redis.Client, log *slog.Logger) *Queue {
	return &Queue{
		client:    asynq.NewClientFromRedisClient(rdb),
		inspector: asynq.NewInspectorFromRedisClient(rdb),
		log:       log,
	}
}

// Enqueue adds tasks and only logs a failure: the request has already
// succeeded, and the sweepers rebuild a lost task from PostgreSQL.
func (q *Queue) Enqueue(ctx context.Context, tasks ...Task) {
	for _, t := range tasks {
		err := q.enqueue(ctx, t)
		if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
			q.log.Error("enqueue_failed", "task", t.Type, "task_id", t.ID, "err", err)
		}
	}
}

// Replace deletes any waiting task with the same id first, so a task that
// moved in time runs at its new time. A running task is left alone.
func (q *Queue) Replace(ctx context.Context, tasks ...Task) {
	q.Remove(tasks...)
	q.Enqueue(ctx, tasks...)
}

// Remove deletes the waiting tasks with these ids, ignoring any not there.
// A failure is only logged: every handler re-checks its row.
func (q *Queue) Remove(tasks ...Task) {
	for _, t := range tasks {
		err := q.inspector.DeleteTask(q.queueName(t), t.ID)
		if err != nil && !errors.Is(err, asynq.ErrTaskNotFound) && !errors.Is(err, asynq.ErrQueueNotFound) {
			q.log.Error("remove_failed", "task", t.Type, "task_id", t.ID, "err", err)
		}
	}
}

func (q *Queue) enqueue(ctx context.Context, t Task) error {
	payload, err := json.Marshal(t.Payload)
	if err != nil {
		return err
	}
	opts := []asynq.Option{asynq.Queue(q.queueName(t))}
	if t.ID != "" {
		opts = append(opts, asynq.TaskID(t.ID))
	}
	if !t.ProcessAt.IsZero() {
		opts = append(opts, asynq.ProcessAt(t.ProcessAt))
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(t.Type, payload), opts...)
	return err
}

func (q *Queue) queueName(t Task) string {
	return q.ns + cmp.Or(t.Queue, Default)
}
