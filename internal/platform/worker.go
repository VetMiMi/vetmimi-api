package platform

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

const (
	// Four tasks at once is ample for tens of appointments a month and keeps
	// the worker light on the 2 GB live host.
	workerConcurrency = 4
	// Docker kills a container ten seconds after SIGTERM. A task still running
	// after seven goes back to its queue and runs again on the next start,
	// which every handler tolerates, leaving time for the rest of shutdown.
	workerShutdownTimeout = 7 * time.Second
)

// Worker runs the task handlers and periodic tasks that domain packages
// register, so cmd/api only wires packages together. It never serves HTTP.
type Worker struct {
	rdb      *redis.Client
	log      *slog.Logger
	mux      *asynq.ServeMux
	periodic []periodicTask
	// ns prefixes every queue name, as in Queue. idlePoll is how long the
	// worker waits before looking at empty queues again; zero is asynq's one
	// second. Tests set both, the second so they run in a fraction of that.
	ns       string
	idlePoll time.Duration
}

type periodicTask struct{ spec, taskType string }

// NewWorker returns a Worker that serves the queues in Redis through rdb.
func NewWorker(rdb *redis.Client, log *slog.Logger) *Worker {
	w := &Worker{rdb: rdb, log: log, mux: asynq.NewServeMux()}
	w.mux.Use(w.logTask)
	return w
}

// Handle runs fn for every task of taskType, with the task's JSON payload. An
// error makes asynq retry the task later with backoff.
func (w *Worker) Handle(taskType string, fn func(ctx context.Context, payload []byte) error) {
	w.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		return fn(ctx, t.Payload())
	})
}

// Every enqueues a task of taskType, with no payload, on the default queue
// on spec: a cron line in UTC or a descriptor such as "@every 5m".
func (w *Worker) Every(spec, taskType string) {
	w.periodic = append(w.periodic, periodicTask{spec, taskType})
}

// Run processes tasks until ctx is done. It then stops the periodic tasks and
// waits for running tasks to finish, for up to workerShutdownTimeout, before
// it returns.
func (w *Worker) Run(ctx context.Context) error {
	w.warnIfRedisMayEvict(ctx)

	// The scheduler gets connections of its own because it closes its client
	// on shutdown, and closing a shared one fails with an error log.
	scheduler := asynq.NewScheduler(ownConnections{w.rdb.Options()}, &asynq.SchedulerOpts{
		Logger: asynqLogger{w.log},
	})
	for _, p := range w.periodic {
		task := asynq.NewTask(p.taskType, nil)
		if _, err := scheduler.Register(p.spec, task, asynq.Queue(w.ns+QueueDefault)); err != nil {
			return fmt.Errorf("worker: schedule %s: %w", p.taskType, err)
		}
	}
	server := asynq.NewServerFromRedisClient(w.rdb, asynq.Config{
		Concurrency: workerConcurrency,
		Queues: map[string]int{
			w.ns + QueueCritical: 6,
			w.ns + QueueDefault:  3,
			w.ns + QueueLow:      1,
		},
		ShutdownTimeout:   workerShutdownTimeout,
		TaskCheckInterval: w.idlePoll,
		Logger:            asynqLogger{w.log},
	})

	if err := server.Start(w.mux); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	if err := scheduler.Start(); err != nil {
		server.Shutdown()
		return fmt.Errorf("worker: %w", err)
	}
	<-ctx.Done()
	scheduler.Shutdown()
	server.Shutdown()
	return nil
}

// warnIfRedisMayEvict checks the eviction policy once at start-up. asynq
// keeps queued tasks as ordinary keys, which Redis deletes under memory
// pressure with any policy but noeviction. Compose sets it on the live host;
// this catches a Redis where it was not set.
func (w *Worker) warnIfRedisMayEvict(ctx context.Context) {
	config, err := w.rdb.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		w.log.Warn("could not read redis maxmemory-policy; queued tasks are lost if redis evicts them", "err", err)
		return
	}
	if policy := config["maxmemory-policy"]; policy != "noeviction" {
		w.log.Warn("redis maxmemory-policy is not noeviction; queued tasks are lost if redis evicts them",
			"maxmemory_policy", policy)
	}
}

// logTask writes one line per processed task. The payload is never logged:
// ids are not useful without the database, and a mistaken payload could
// carry personal data.
func (w *Worker) logTask(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, t *asynq.Task) error {
		start := time.Now()
		err := next.ProcessTask(ctx, t)
		id, _ := asynq.GetTaskID(ctx)
		attrs := []any{"task", t.Type(), "task_id", id, "duration_ms", time.Since(start).Milliseconds()}
		if err != nil {
			w.log.Error("task processed", append(attrs, "outcome", "failed", "err", err)...)
			return err
		}
		w.log.Info("task processed", append(attrs, "outcome", "ok")...)
		return nil
	})
}

// ownConnections makes asynq open a pool of its own with the same options.
type ownConnections struct{ opts *redis.Options }

func (o ownConnections) MakeRedisClient() any {
	opts := *o.opts
	return redis.NewClient(&opts)
}

// asynqLogger sends asynq's own messages through slog, so the worker writes
// one JSON line per event like the rest of the process.
type asynqLogger struct{ log *slog.Logger }

func (l asynqLogger) Debug(args ...any) { l.log.Debug(fmt.Sprint(args...)) }
func (l asynqLogger) Info(args ...any)  { l.log.Info(fmt.Sprint(args...)) }
func (l asynqLogger) Warn(args ...any)  { l.log.Warn(fmt.Sprint(args...)) }
func (l asynqLogger) Error(args ...any) { l.log.Error(fmt.Sprint(args...)) }

// Fatal honours asynq's contract that it does not return.
func (l asynqLogger) Fatal(args ...any) {
	l.log.Error(fmt.Sprint(args...))
	os.Exit(1)
}
