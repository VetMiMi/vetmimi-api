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

// Queue adds tasks to the asynq queues in Redis. A nil Queue, as in tests, does nothing.
type Queue struct {
	client    *asynq.Client
	inspector *asynq.Inspector
	log       *slog.Logger
	ns        string // queue-name prefix, set only by tests
}

// The worker takes about six Critical tasks for every three Default and one Low.
const (
	Critical = "critical"
	Default  = "default"
	Low      = "low"
)

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
	if q == nil {
		return
	}
	for _, task := range tasks {
		err := q.enqueue(ctx, task)
		if err != nil && !errors.Is(err, asynq.ErrTaskIDConflict) {
			q.log.Error("enqueue_failed", "task", task.Type, "task_id", task.ID, "err", err)
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
	if q == nil {
		return
	}
	for _, task := range tasks {
		err := q.inspector.DeleteTask(q.queueName(task), task.ID)
		if err != nil && !errors.Is(err, asynq.ErrTaskNotFound) && !errors.Is(err, asynq.ErrQueueNotFound) {
			q.log.Error("remove_failed", "task", task.Type, "task_id", task.ID, "err", err)
		}
	}
}

func (q *Queue) enqueue(ctx context.Context, task Task) error {
	payload, err := json.Marshal(task.Payload)
	if err != nil {
		return err
	}
	options := []asynq.Option{asynq.Queue(q.queueName(task))}
	if task.ID != "" {
		options = append(options, asynq.TaskID(task.ID))
	}
	if !task.ProcessAt.IsZero() {
		options = append(options, asynq.ProcessAt(task.ProcessAt))
	}
	_, err = q.client.EnqueueContext(ctx, asynq.NewTask(task.Type, payload), options...)
	return err
}

func (q *Queue) queueName(task Task) string {
	return q.ns + cmp.Or(task.Queue, Default)
}
