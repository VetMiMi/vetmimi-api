package queue

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/redistest"
)

// testWorker is a Worker and a Queue on REDIS_URL_TEST, in a namespace of
// the test's own, logging to a buffer the test can read.
type testWorker struct {
	*Worker
	queue *Queue
	logs  *logBuffer
}

func newTestWorker(t *testing.T) *testWorker {
	t.Helper()
	rdb := redistest.Client(t)
	ns := testNamespace(t, rdb)
	logs := &logBuffer{}
	log := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	w := NewWorker(rdb, log)
	w.ns = ns
	w.idlePoll = 50 * time.Millisecond
	q := New(rdb, log)
	q.ns = ns
	return &testWorker{Worker: w, queue: q, logs: logs}
}

// start runs the worker until the test ends and checks that it stops cleanly.
func (tw *testWorker) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tw.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		require.NoError(t, <-done)
	})
}

func TestWorkerServesEveryQueue(t *testing.T) {
	tw := newTestWorker(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	tw.Handle("test:queue", func(_ context.Context, payload []byte) error {
		mu.Lock()
		defer mu.Unlock()
		seen[string(payload)] = true
		return nil
	})
	tw.start(t)

	for _, queue := range []string{Critical, Default, Low} {
		tw.queue.Enqueue(context.Background(), Task{Type: "test:queue", Queue: queue, Payload: queue})
	}
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return seen[`"critical"`] && seen[`"default"`] && seen[`"low"`]
	}, 5*time.Second, 50*time.Millisecond)
}

func TestWorkerRunsFourTasksAtOnce(t *testing.T) {
	tw := newTestWorker(t)
	var running atomic.Int32
	release := make(chan struct{})
	tw.Handle("test:block", func(context.Context, []byte) error {
		running.Add(1)
		<-release
		return nil
	})
	for range 5 {
		tw.queue.Enqueue(context.Background(), Task{Type: "test:block"})
	}
	tw.start(t)
	// Registered after start, so it runs first and the worker can stop.
	t.Cleanup(func() { close(release) })

	require.Eventually(t, func() bool { return running.Load() >= 4 }, 5*time.Second, 20*time.Millisecond)
	// A fifth slot would be filled at once: the queue is not empty, so the
	// worker does not wait between fetches.
	time.Sleep(300 * time.Millisecond)
	require.EqualValues(t, 4, running.Load())
}

func TestPeriodicTaskRuns(t *testing.T) {
	tw := newTestWorker(t)
	var ticks atomic.Int32
	tw.Handle("test:tick", func(context.Context, []byte) error {
		ticks.Add(1)
		return nil
	})
	tw.Every("@every 1s", "test:tick")
	tw.start(t)

	require.Eventually(t, func() bool { return ticks.Load() >= 1 }, 5*time.Second, 50*time.Millisecond)
}

func TestInvalidScheduleStopsTheWorker(t *testing.T) {
	tw := newTestWorker(t)
	tw.Every("every five minutes", "test:never")

	// The timeout only bounds a broken Run that would otherwise wait forever.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.ErrorContains(t, tw.Run(ctx), "schedule test:never")
}

// cmd/api cancels Run's context on SIGTERM.
func TestShutdownLetsARunningTaskFinish(t *testing.T) {
	tw := newTestWorker(t)
	started := make(chan struct{})
	var once sync.Once
	var finished atomic.Bool
	tw.Handle("test:slow", func(ctx context.Context, _ []byte) error {
		once.Do(func() { close(started) })
		select {
		case <-time.After(time.Second):
			finished.Store(true)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	tw.queue.Enqueue(context.Background(), Task{Type: "test:slow", ID: "slow-1"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tw.Run(ctx) }()

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the task never started")
	}
	stopping := time.Now()
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not stop within 10 seconds")
	}

	require.True(t, finished.Load(), "shutdown cut the running task short")
	require.GreaterOrEqual(t, time.Since(stopping), 500*time.Millisecond, "Run returned before the task could finish")
	entry := tw.waitForLog(t, "task processed")
	require.Equal(t, "slow-1", entry["task_id"])
	require.Equal(t, "ok", entry["outcome"])
}

func TestFailedTaskIsLogged(t *testing.T) {
	tw := newTestWorker(t)
	tw.Handle("test:fail", func(context.Context, []byte) error {
		return errors.New("resend answered 500")
	})
	tw.start(t)
	tw.queue.Enqueue(context.Background(), Task{Type: "test:fail", ID: "fail-1", Payload: "payload-marker"})

	entry := tw.waitForLog(t, "task processed")
	require.Equal(t, "test:fail", entry["task"])
	require.Equal(t, "fail-1", entry["task_id"])
	require.Equal(t, "failed", entry["outcome"])
	require.Equal(t, "resend answered 500", entry["err"])
	require.NotContains(t, tw.logs.String(), "payload-marker", "the payload was logged")
}

// redisConfig answers CONFIG GET itself, so the test never changes a real
// Redis's server-wide policy.
type redisConfig struct {
	policy string
	err    error
}

func (c redisConfig) DialHook(next redis.DialHook) redis.DialHook { return next }

func (c redisConfig) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func (c redisConfig) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		get, ok := cmd.(*redis.MapStringStringCmd)
		if !ok || cmd.Name() != "config" {
			return next(ctx, cmd)
		}
		if c.err != nil {
			get.SetErr(c.err)
			return c.err
		}
		get.SetVal(map[string]string{"maxmemory-policy": c.policy})
		return nil
	}
}

func TestWorkerWarnsUnlessRedisKeepsQueuedTasks(t *testing.T) {
	for name, tc := range map[string]struct {
		config redisConfig
		warn   string
	}{
		"noeviction":  {redisConfig{policy: "noeviction"}, ""},
		"allkeys-lru": {redisConfig{policy: "allkeys-lru"}, "redis maxmemory-policy is not noeviction; queued tasks are lost if redis evicts them"},
		"unreadable":  {redisConfig{err: errors.New("ERR unknown command 'CONFIG'")}, "could not read redis maxmemory-policy; queued tasks are lost if redis evicts them"},
	} {
		t.Run(name, func(t *testing.T) {
			rdb := redistest.Stopped(t)
			rdb.AddHook(tc.config)
			logs := &logBuffer{}
			w := NewWorker(rdb, slog.New(slog.NewJSONHandler(logs, nil)))

			w.warnIfRedisMayEvict(context.Background())

			if tc.warn == "" {
				require.Empty(t, logs.String())
				return
			}
			entries := logs.entries(tc.warn)
			require.Len(t, entries, 1, logs.String())
			require.Equal(t, "WARN", entries[0]["level"])
		})
	}
}

// testNamespace returns a queue-name prefix of the test's own and deletes
// every key under it when the test ends.
func testNamespace(t *testing.T, rdb *redis.Client) string {
	t.Helper()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	ns := "test-" + hex.EncodeToString(random) + "-"

	t.Cleanup(func() {
		ctx := context.Background()
		var keys []string
		iter := rdb.Scan(ctx, 0, "asynq:{"+ns+"*", 100).Iterator()
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		require.NoError(t, iter.Err())
		if len(keys) > 0 {
			require.NoError(t, rdb.Del(ctx, keys...).Err())
		}
		require.NoError(t, rdb.SRem(ctx, "asynq:queues", ns+Critical, ns+Default, ns+Low).Err())
	})
	return ns
}

// logBuffer collects JSON log lines from goroutines asynq starts.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// entries returns the decoded lines whose message is msg. It may run in a
// require.Eventually goroutine, where a test cannot fail, so it panics on a
// line slog could not have written.
func (b *logBuffer) entries(msg string) []map[string]any {
	var found []map[string]any
	for line := range strings.Lines(b.String()) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			panic(err)
		}
		if entry["msg"] == msg {
			found = append(found, entry)
		}
	}
	return found
}
