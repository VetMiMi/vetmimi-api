package content

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

// Tasks runs the publishing worker.
type Tasks struct {
	Pool             *pgxpool.Pool
	Queue            *queue.Queue         // nil drops the tasks, as in tests
	Publishers       map[string]Publisher // a channel missing here is not connected
	SiteURL          string
	RevalidateSecret string       // empty skips revalidation, as in development
	HTTP             *http.Client // nil means a client with a 10-second timeout
	Log              *slog.Logger
	Now              clock.Now
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskPublishScheduled, t.PublishScheduled)
	w.Handle(TaskPublishChannel, t.PublishChannel)
	w.Handle(TaskRevalidate, t.Revalidate)
	w.Handle(TaskSweep, t.Sweep)
	w.Every("@every 5m", TaskSweep)
}

func (t *Tasks) PublishScheduled(ctx context.Context, payload []byte) error {
	id, _, err := parsePublishPayload(payload)
	if err != nil {
		return err
	}
	post, err := PublishScheduled(ctx, t.Pool, id, t.Now())
	if err != nil {
		return err
	}
	t.enqueue(ctx, append(PublishTasks(post), RevalidateTasks(post)...)...)
	return nil
}

func (t *Tasks) PublishChannel(ctx context.Context, payload []byte) error {
	id, channel, err := parsePublishPayload(payload)
	if err != nil {
		return err
	}
	publish, ok := t.Publishers[channel]
	if !ok {
		publish = NotConnected
	}
	return PublishChannel(ctx, t.Pool, id, channel, publish, t.Now())
}

func (t *Tasks) Sweep(ctx context.Context, _ []byte) error {
	q := db.New(t.Pool)
	failed, err := FailStuck(ctx, q, t.Now())
	if err != nil {
		return err
	}
	tasks, err := DueTasks(ctx, q, t.Now())
	if err != nil {
		return err
	}
	t.enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "publishing swept", "found", len(tasks), "unknown_outcome", failed)
	return nil
}

// Revalidate is harmless to repeat, so a failure is simply retried.
func (t *Tasks) Revalidate(ctx context.Context, payload []byte) error {
	var p revalidatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("content: revalidate payload: %w", err)
	}
	if t.RevalidateSecret == "" {
		t.Log.InfoContext(ctx, "revalidation skipped", "reason", "SITE_REVALIDATE_SECRET is empty")
		return nil
	}
	return t.callRevalidate(ctx, p.Slug)
}

func (t *Tasks) callRevalidate(ctx context.Context, slug string) error {
	body, err := json.Marshal(map[string][]string{"tags": {"articles", "article:" + slug}})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(t.SiteURL, "/")+"/api/revalidate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Revalidate-Secret", t.RevalidateSecret)
	client := t.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("content: revalidate: %w", err)
	}
	_ = res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("content: revalidate: site answered %d", res.StatusCode)
	}
	return nil
}

func (t *Tasks) enqueue(ctx context.Context, tasks ...queue.Task) {
	if t.Queue != nil {
		t.Queue.Enqueue(ctx, tasks...)
	}
}

func parsePublishPayload(payload []byte) (pgtype.UUID, string, error) {
	var p publishPayload
	var id pgtype.UUID
	if err := json.Unmarshal(payload, &p); err != nil {
		return id, "", fmt.Errorf("content: task payload: %w", err)
	}
	if err := id.Scan(p.PostID); err != nil {
		return id, "", fmt.Errorf("content: task payload: %w", err)
	}
	return id, p.Channel, nil
}
