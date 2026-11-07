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

const (
	TaskPublishScheduled = "content:publish-scheduled"
	TaskPublishChannel   = "content:publish-channel"
	TaskRevalidate       = "content:revalidate"
	TaskSweep            = "content:sweep"
)

const (
	stuckAfter    = 10 * time.Minute // far longer than any attempt; then the sweep steps in
	scheduleGrace = time.Minute      // a post just due is left to its own task
	sweepLimit    = 100
)

type publishPayload struct {
	PostID  string `json:"postId"`
	Channel string `json:"channel,omitempty"`
}

type revalidatePayload struct {
	Slug string `json:"slug"`
}

// Tasks runs the publishing worker.
type Tasks struct {
	Pool             *pgxpool.Pool
	Queue            *queue.Queue
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

// PublishScheduled is safe to repeat: a post already published is no longer scheduled.
func (t *Tasks) PublishScheduled(ctx context.Context, payload []byte) error {
	postID, _, err := parsePublishPayload(payload)
	if err != nil {
		return err
	}

	post, err := publishDuePost(ctx, t.Pool, postID, t.Now())
	if err != nil {
		return err
	}

	t.Queue.Enqueue(ctx, PublishTasks(post)...)
	t.Queue.Enqueue(ctx, RevalidateTasks(post)...)
	return nil
}

// PublishChannel is safe to repeat: a channel is claimed once per attempt.
func (t *Tasks) PublishChannel(ctx context.Context, payload []byte) error {
	postID, channel, err := parsePublishPayload(payload)
	if err != nil {
		return err
	}

	publish, connected := t.Publishers[channel]
	if !connected {
		publish = NotConnected
	}
	return publishToChannel(ctx, t.Pool, postID, channel, publish, t.Now())
}

// Revalidate is harmless to repeat, so a failure is simply retried.
func (t *Tasks) Revalidate(ctx context.Context, payload []byte) error {
	var p revalidatePayload
	err := json.Unmarshal(payload, &p)
	if err != nil {
		return fmt.Errorf("content: revalidate payload: %w", err)
	}

	if t.RevalidateSecret == "" {
		t.Log.InfoContext(ctx, "revalidation skipped", "reason", "SITE_REVALIDATE_SECRET is empty")
		return nil
	}
	return t.callRevalidate(ctx, p.Slug)
}

// Sweep rebuilds tasks Redis lost and gives up on attempts that never finished.
func (t *Tasks) Sweep(ctx context.Context, _ []byte) error {
	q := db.New(t.Pool)
	now := t.Now()

	failed, err := failStuckPublications(ctx, q, now)
	if err != nil {
		return err
	}
	tasks, err := DueTasks(ctx, q, now)
	if err != nil {
		return err
	}

	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "publishing swept", "found", len(tasks), "unknown_outcome", failed)
	return nil
}

// ScheduledTask is the post's publish task; one left behind by a moved schedule does nothing.
func ScheduledTask(post Post) []queue.Task {
	if post.Status != "scheduled" {
		return nil
	}
	return []queue.Task{newScheduledTask(post.ID, post.ScheduledAt.Time)}
}

func PublishTasks(post Post) []queue.Task {
	var tasks []queue.Task
	for _, publication := range post.Publications {
		if publication.Status == "pending" {
			tasks = append(tasks, newChannelTask(publication))
		}
	}
	return tasks
}

// RevalidateTasks is empty until the website is live: an edit alone changes nothing visitors read.
func RevalidateTasks(post Post) []queue.Task {
	website := channelVersion(post.Versions, "website")
	if !website.Slug.Valid || publicationOf(post, "website").Status != "published" {
		return nil
	}
	task := queue.Task{
		Type:    TaskRevalidate,
		Payload: revalidatePayload{Slug: website.Slug.String},
	}
	return []queue.Task{task}
}

// DueTasks rebuilds the tasks Redis lost, with their first ids so none is added twice.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]queue.Task, error) {
	posts, err := q.ListDueScheduledPosts(ctx, db.ListDueScheduledPostsParams{
		Before:  now.Add(-scheduleGrace),
		MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}
	publications, err := q.ListLostPostPublications(ctx, db.ListLostPostPublicationsParams{
		Before:  now.Add(-stuckAfter),
		MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}

	var tasks []queue.Task
	for _, post := range posts {
		tasks = append(tasks, newScheduledTask(post.ID, post.ScheduledAt.Time))
	}
	for _, publication := range publications {
		tasks = append(tasks, newChannelTask(publication))
	}
	return tasks, nil
}

// failStuckPublications marks long-running attempts unknown_outcome and never re-sends them:
// the worker died mid-call, so the post may already be on the platform.
func failStuckPublications(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.FailStuckPostPublications(ctx, db.FailStuckPostPublicationsParams{
		Before: now.Add(-stuckAfter),
		Now:    now,
	})
}

func newScheduledTask(postID pgtype.UUID, at time.Time) queue.Task {
	return queue.Task{
		Type:      TaskPublishScheduled,
		ID:        fmt.Sprintf("post:%s:%d", postID, at.Unix()),
		Payload:   publishPayload{PostID: postID.String()},
		ProcessAt: at,
	}
}

// The id names the attempt, so a retry is a new task but a duplicate is not added.
func newChannelTask(publication db.PostPublication) queue.Task {
	return queue.Task{
		Type:    TaskPublishChannel,
		ID:      fmt.Sprintf("publish:%s:%s:%d", publication.PostID, publication.Channel, publication.Attempts),
		Payload: publishPayload{PostID: publication.PostID.String(), Channel: publication.Channel},
	}
}

func (t *Tasks) callRevalidate(ctx context.Context, slug string) error {
	body, err := json.Marshal(map[string][]string{"tags": {"articles", "article:" + slug}})
	if err != nil {
		return err
	}
	url := strings.TrimSuffix(t.SiteURL, "/") + "/api/revalidate"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
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
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("content: revalidate: site answered %d", res.StatusCode)
	}
	return nil
}

func parsePublishPayload(payload []byte) (pgtype.UUID, string, error) {
	var p publishPayload
	var postID pgtype.UUID
	err := json.Unmarshal(payload, &p)
	if err != nil {
		return postID, "", fmt.Errorf("content: task payload: %w", err)
	}
	err = postID.Scan(p.PostID)
	if err != nil {
		return postID, "", fmt.Errorf("content: task payload: %w", err)
	}
	return postID, p.Channel, nil
}

func publicationOf(post Post, channel string) db.PostPublication {
	for _, publication := range post.Publications {
		if publication.Channel == channel {
			return publication
		}
	}
	return db.PostPublication{}
}
