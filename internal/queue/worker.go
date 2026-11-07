package queue

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/redis/go-redis/v9"
)

// Worker runs the task handlers and periodic tasks domain packages register.
type Worker struct {
	rdb      *redis.Client
	log      *slog.Logger
	mux      *asynq.ServeMux
	periodic []periodicTask
	// Set only by tests: a queue-name prefix, and how long to wait before
	// polling empty queues again (zero is asynq's one second).
	ns       string
	idlePoll time.Duration
}

type periodicTask struct {
	spec     string
	taskType string
}

// ownConnections makes asynq open a pool of its own with the same options.
type ownConnections struct {
	options *redis.Options
}

// asynqLogger sends asynq's own messages through slog.
type asynqLogger struct {
	log *slog.Logger
}

const (
	// Ample for tens of appointments a month on the 2 GB live host.
	workerConcurrency = 4
	// Docker kills a container ten seconds after SIGTERM. A task still running
	// after seven goes back to its queue and runs again on the next start.
	workerShutdownTimeout = 7 * time.Second
)

func NewWorker(rdb *redis.Client, log *slog.Logger) *Worker {
	w := &Worker{
		rdb: rdb,
		log: log,
		mux: asynq.NewServeMux(),
	}
	w.mux.Use(w.logTask)
	return w
}

// Handle runs handler for every task of taskType. An error makes asynq retry
// the task later with backoff.
func (w *Worker) Handle(taskType string, handler func(ctx context.Context, payload []byte) error) {
	w.mux.HandleFunc(taskType, func(ctx context.Context, task *asynq.Task) error {
		return handler(ctx, task.Payload())
	})
}

// Every enqueues a task of taskType on the Default queue on spec: a cron line
// in UTC, one with a CRON_TZ= prefix, or a descriptor such as "@every 5m".
func (w *Worker) Every(spec, taskType string) {
	w.periodic = append(w.periodic, periodicTask{spec: spec, taskType: taskType})
}

// Run processes tasks until ctx is done, then waits up to
// workerShutdownTimeout for running tasks to finish.
func (w *Worker) Run(ctx context.Context) error {
	w.warnIfRedisMayEvict(ctx)

	scheduler, err := w.newScheduler()
	if err != nil {
		return err
	}
	server := w.newServer()
	err = server.Start(w.mux)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	err = scheduler.Start()
	if err != nil {
		server.Shutdown()
		return fmt.Errorf("worker: %w", err)
	}

	<-ctx.Done()
	scheduler.Shutdown()
	server.Shutdown()
	return nil
}

func (w *Worker) newScheduler() (*asynq.Scheduler, error) {
	// The scheduler closes its client on shutdown, so it gets connections of
	// its own; closing the shared client would fail with an error log.
	connections := ownConnections{options: w.rdb.Options()}
	scheduler := asynq.NewScheduler(connections, &asynq.SchedulerOpts{
		Logger: asynqLogger{log: w.log},
	})
	for _, periodic := range w.periodic {
		task := asynq.NewTask(periodic.taskType, nil)
		_, err := scheduler.Register(periodic.spec, task, asynq.Queue(w.ns+Default))
		if err != nil {
			return nil, fmt.Errorf("worker: schedule %s: %w", periodic.taskType, err)
		}
	}
	return scheduler, nil
}

func (w *Worker) newServer() *asynq.Server {
	return asynq.NewServerFromRedisClient(w.rdb, asynq.Config{
		Concurrency: workerConcurrency,
		Queues: map[string]int{
			w.ns + Critical: 6,
			w.ns + Default:  3,
			w.ns + Low:      1,
		},
		ShutdownTimeout:   workerShutdownTimeout,
		TaskCheckInterval: w.idlePoll,
		Logger:            asynqLogger{log: w.log},
	})
}

// warnIfRedisMayEvict warns when Redis may delete queued tasks under memory
// pressure, which it does with any maxmemory-policy but noeviction.
func (w *Worker) warnIfRedisMayEvict(ctx context.Context) {
	config, err := w.rdb.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		w.log.Warn("could not read redis maxmemory-policy; queued tasks are lost if redis evicts them", "err", err)
		return
	}
	policy := config["maxmemory-policy"]
	if policy != "noeviction" {
		w.log.Warn("redis maxmemory-policy is not noeviction; queued tasks are lost if redis evicts them",
			"maxmemory_policy", policy)
	}
}

// logTask writes one line per task. The payload is never logged, in case a
// mistaken one carries personal data.
func (w *Worker) logTask(next asynq.Handler) asynq.Handler {
	return asynq.HandlerFunc(func(ctx context.Context, task *asynq.Task) error {
		start := time.Now()
		err := next.ProcessTask(ctx, task)
		id, _ := asynq.GetTaskID(ctx)
		attributes := []any{"task", task.Type(), "task_id", id, "duration_ms", time.Since(start).Milliseconds()}
		if err != nil {
			w.log.Error("task processed", append(attributes, "outcome", "failed", "err", err)...)
			return err
		}
		w.log.Info("task processed", append(attributes, "outcome", "ok")...)
		return nil
	})
}

func (connections ownConnections) MakeRedisClient() any {
	options := *connections.options
	return redis.NewClient(&options)
}

func (logger asynqLogger) Debug(args ...any) {
	logger.log.Debug(fmt.Sprint(args...))
}

func (logger asynqLogger) Info(args ...any) {
	logger.log.Info(fmt.Sprint(args...))
}

func (logger asynqLogger) Warn(args ...any) {
	logger.log.Warn(fmt.Sprint(args...))
}

func (logger asynqLogger) Error(args ...any) {
	logger.log.Error(fmt.Sprint(args...))
}

// Fatal honours asynq's contract that it does not return.
func (logger asynqLogger) Fatal(args ...any) {
	logger.log.Error(fmt.Sprint(args...))
	os.Exit(1)
}
