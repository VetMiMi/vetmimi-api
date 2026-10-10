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

// Tasks runs the publishing worker: scheduled posts, one task per social
// channel, site revalidation, and the sweep that rebuilds lost tasks.
type Tasks struct {
	Pool *pgxpool.Pool
	// Queue takes the tasks a handler leads to; nil drops them, as in tests.
	Queue *queue.Queue
	// Publishers posts each social channel; a channel missing from it is
	// not connected.
	Publishers map[string]Publisher
	// SiteURL and RevalidateSecret reach the site's POST /api/revalidate;
	// without the secret, as in development, revalidation is skipped.
	SiteURL          string
	RevalidateSecret string
	// HTTP calls the site; nil means a client with a 10-second timeout.
	HTTP *http.Client
	Log  *slog.Logger
	Now  clock.Now
}

// Register adds the publishing handlers and the sweep schedule to w.
func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskPublishScheduled, t.PublishScheduled)
	w.Handle(TaskPublishChannel, t.PublishChannel)
	w.Handle(TaskRevalidate, t.Revalidate)
	w.Handle(TaskSweep, t.Sweep)
	w.Every("@every 5m", TaskSweep)
}

// PublishScheduled starts publishing a scheduled post whose time has come.
func (t *Tasks) PublishScheduled(ctx context.Context, payload []byte) error {
	var p postPayload
	id, err := decodeID(payload, &p, &p.PostID)
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

// PublishChannel makes one attempt at a social channel.
func (t *Tasks) PublishChannel(ctx context.Context, payload []byte) error {
	var p channelPayload
	id, err := decodeID(payload, &p, &p.PostID)
	if err != nil {
		return err
	}
	publish, ok := t.Publishers[p.Channel]
	if !ok {
		publish = NotConnected
	}
	return PublishChannel(ctx, t.Pool, id, p.Channel, publish, t.Now())
}

// Sweep fails the channels FailStuck finds and enqueues DueTasks.
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

// Revalidate asks the site to drop its cached article list and the one
// article. Revalidating twice is harmless, so a failure is simply retried.
func (t *Tasks) Revalidate(ctx context.Context, payload []byte) error {
	var p revalidatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("content: revalidate payload: %w", err)
	}
	if t.RevalidateSecret == "" {
		t.Log.InfoContext(ctx, "revalidation skipped", "reason", "SITE_REVALIDATE_SECRET is empty")
		return nil
	}
	body, err := json.Marshal(map[string][]string{"tags": {"articles", "article:" + p.Slug}})
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

// decodeID unmarshals payload into v and parses the post id it holds.
func decodeID(payload []byte, v any, postID *string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := json.Unmarshal(payload, v); err != nil {
		return id, fmt.Errorf("content: task payload: %w", err)
	}
	if err := id.Scan(*postID); err != nil {
		return id, fmt.Errorf("content: task payload: %w", err)
	}
	return id, nil
}
