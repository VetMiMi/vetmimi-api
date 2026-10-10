package queue

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/redistest"
)

func TestEnqueuedTaskIsProcessed(t *testing.T) {
	tw := newTestWorker(t)
	received := make(chan []byte, 1)
	tw.Handle("test:echo", func(_ context.Context, payload []byte) error {
		received <- payload
		return nil
	})
	tw.start(t)

	tw.queue.Enqueue(context.Background(), Task{
		Type:    "test:echo",
		ID:      "echo-1",
		Payload: map[string]string{"appointment_id": "payload-marker"},
	})

	select {
	case payload := <-received:
		require.JSONEq(t, `{"appointment_id":"payload-marker"}`, string(payload))
	case <-time.After(5 * time.Second):
		t.Fatal("the task was not processed")
	}
	entry := tw.waitForLog(t, "task processed")
	require.Equal(t, "test:echo", entry["task"])
	require.Equal(t, "echo-1", entry["task_id"])
	require.Equal(t, "ok", entry["outcome"])
	require.Contains(t, entry, "duration_ms")
	require.NotContains(t, tw.logs.String(), "payload-marker", "the payload was logged")
}

func TestEnqueueHonoursQueueAndProcessAt(t *testing.T) {
	tw := newTestWorker(t)
	tw.queue.Enqueue(context.Background(),
		Task{Type: "test:later", Queue: Critical, ProcessAt: time.Now().Add(time.Hour)},
		Task{Type: "test:now"},
	)

	critical := tw.queueInfo(t, Critical)
	require.Equal(t, 1, critical.Scheduled)
	require.Equal(t, 0, critical.Pending)
	require.Equal(t, 1, tw.queueInfo(t, Default).Pending, "an empty Queue means default")
}

func TestDuplicateTaskIDRunsOnce(t *testing.T) {
	tw := newTestWorker(t)
	var runs atomic.Int32
	tw.Handle("test:once", func(context.Context, []byte) error {
		runs.Add(1)
		return nil
	})

	ctx := context.Background()
	task := Task{Type: "test:once", ID: "comms:42"}
	tw.queue.Enqueue(ctx, task)
	tw.queue.Enqueue(ctx, task)
	require.Equal(t, 1, tw.queueInfo(t, Default).Pending)
	require.Empty(t, tw.logs.entries("enqueue_failed"), "a task-id conflict is not a failure")

	tw.start(t)
	inspector := asynq.NewInspectorFromRedisClient(tw.rdb)
	require.Eventually(t, func() bool {
		info, err := inspector.GetQueueInfo(tw.ns + Default)
		return err == nil && runs.Load() >= 1 && info.Pending == 0 && info.Active == 0
	}, 5*time.Second, 50*time.Millisecond)
	require.EqualValues(t, 1, runs.Load())
}

func TestReplaceMovesAWaitingTask(t *testing.T) {
	tw := newTestWorker(t)
	ctx := context.Background()
	first := time.Now().Add(time.Hour).Truncate(time.Second)
	task := Task{Type: "test:remind", ID: "comms:9", Queue: Critical, ProcessAt: first}
	tw.queue.Enqueue(ctx, task)

	task.ProcessAt = first.Add(time.Hour)
	tw.queue.Enqueue(ctx, task)
	inspector := asynq.NewInspectorFromRedisClient(tw.rdb)
	info, err := inspector.GetTaskInfo(tw.ns+Critical, "comms:9")
	require.NoError(t, err)
	require.Equal(t, first, info.NextProcessAt.Truncate(time.Second), "Enqueue keeps the old time")

	tw.queue.Replace(ctx, task, Task{Type: "test:remind", ID: "comms:10", Queue: Critical, ProcessAt: first})
	info, err = inspector.GetTaskInfo(tw.ns+Critical, "comms:9")
	require.NoError(t, err)
	require.Equal(t, first.Add(time.Hour), info.NextProcessAt.Truncate(time.Second))
	require.Equal(t, 2, tw.queueInfo(t, Critical).Scheduled, "a task with no old copy is added")
	require.Empty(t, tw.logs.entries("remove_failed"))
}

// Removing a task already gone is no failure.
func TestRemoveDeletesAWaitingTask(t *testing.T) {
	tw := newTestWorker(t)
	task := Task{Type: "test:hold", ID: "hold:7", ProcessAt: time.Now().Add(time.Hour)}
	tw.queue.Enqueue(context.Background(), task)
	require.Equal(t, 1, tw.queueInfo(t, Default).Scheduled)

	tw.queue.Remove(task, Task{Type: "test:hold", ID: "hold:8"})
	require.Equal(t, 0, tw.queueInfo(t, Default).Scheduled)
	require.Empty(t, tw.logs.entries("remove_failed"))
}

func TestEnqueueFailureIsLoggedNotReturned(t *testing.T) {
	logs := &logBuffer{}
	q := New(redistest.Stopped(t), slog.New(slog.NewJSONHandler(logs, nil)))

	q.Enqueue(context.Background(), Task{Type: "test:lost", ID: "comms:7", Payload: map[string]string{"id": "payload-marker"}})

	entries := logs.entries("enqueue_failed")
	require.Len(t, entries, 1)
	require.Equal(t, "test:lost", entries[0]["task"])
	require.Equal(t, "comms:7", entries[0]["task_id"])
	require.Contains(t, entries[0]["err"], "connection refused")
	require.NotContains(t, logs.String(), "payload-marker")
}

func (tw *testWorker) queueInfo(t *testing.T, queue string) *asynq.QueueInfo {
	t.Helper()
	info, err := asynq.NewInspectorFromRedisClient(tw.rdb).GetQueueInfo(tw.ns + queue)
	require.NoError(t, err)
	return info
}

// waitForLog returns the first line with message msg.
func (tw *testWorker) waitForLog(t *testing.T, msg string) map[string]any {
	t.Helper()
	var entry map[string]any
	require.Eventually(t, func() bool {
		entries := tw.logs.entries(msg)
		if len(entries) > 0 {
			entry = entries[0]
		}
		return entry != nil
	}, 5*time.Second, 50*time.Millisecond, "no %q log line", msg)
	return entry
}
