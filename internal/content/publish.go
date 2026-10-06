package content

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// The publishing worker's tasks (docs/architecture.md, "Background jobs").
const (
	TaskPublishScheduled = "content:publish-scheduled"
	TaskPublishChannel   = "content:publish-channel"
	TaskRevalidate       = "content:revalidate"
	TaskSweep            = "content:sweep"
)

const (
	// maxAttempts is a first try and three retries, which asynq spaces out
	// with its backoff.
	maxAttempts = 4
	// stuckAfter is how long a channel may wait before the sweep enqueues
	// it again, or run before the sweep fails it as unknown_outcome; an
	// attempt, polling included, is over well within it.
	stuckAfter = 10 * time.Minute
	// scheduleGrace leaves a scheduled post that has just come due to its
	// own task.
	scheduleGrace = time.Minute
	sweepLimit    = 100
)

// Posted is where a channel's post went on its platform.
type Posted struct {
	ExternalID, Permalink string
}

// Publisher posts one social channel version to its platform.
type Publisher func(ctx context.Context, v db.PostVersion) (Posted, error)

// ChannelError is why a channel was not posted; Reason is the code the
// portal shows next to copy & open. A transient error is tried again.
type ChannelError struct {
	Reason    string
	Transient bool
}

func (e *ChannelError) Error() string { return "content: channel not posted: " + e.Reason }

// ErrNotConnected is a channel with no platform connection yet.
var ErrNotConnected = &ChannelError{Reason: "not_connected"}

// NotConnected is the Publisher of a channel whose connector does not exist
// yet (LinkedIn, until #122): Daw Mi posts it by hand.
func NotConnected(context.Context, db.PostVersion) (Posted, error) { return Posted{}, ErrNotConnected }

type postPayload struct {
	PostID string `json:"postId"`
}

type channelPayload struct {
	PostID  string `json:"postId"`
	Channel string `json:"channel"`
}

type revalidatePayload struct {
	Slug string `json:"slug"`
}

func scheduledTask(id pgtype.UUID, at time.Time) platform.Task {
	return platform.Task{Type: TaskPublishScheduled, ID: fmt.Sprintf("post:%s:%d", id, at.Unix()),
		Payload: postPayload{PostID: id.String()}, ProcessAt: at}
}

// channelTask's id names the attempt, so a retry after a final failure is a
// new task, while a duplicate of a waiting one is not added.
func channelTask(pub db.PostPublication) platform.Task {
	return platform.Task{Type: TaskPublishChannel,
		ID:      fmt.Sprintf("publish:%s:%s:%d", pub.PostID, pub.Channel, pub.Attempts),
		Payload: channelPayload{PostID: pub.PostID.String(), Channel: pub.Channel}}
}

// ScheduledTask publishes p at its scheduled time. A task left behind by a
// moved or cancelled schedule finds the post not due and does nothing.
func ScheduledTask(p Post) []platform.Task {
	if p.Status != "scheduled" {
		return nil
	}
	return []platform.Task{scheduledTask(p.ID, p.ScheduledAt.Time)}
}

// PublishTasks are the tasks for p's channels waiting for the worker.
func PublishTasks(p Post) []platform.Task {
	var out []platform.Task
	for _, pub := range p.Publications {
		if pub.Status == "pending" {
			out = append(out, channelTask(pub))
		}
	}
	return out
}

// RevalidateTasks asks the site to refresh p's article once p has been on
// the website: after it goes live, goes live again after an edit, or is
// archived. An edit alone changes nothing visitors read.
func RevalidateTasks(p Post) []platform.Task {
	for _, v := range p.Versions {
		if v.Channel == "website" && v.Slug.Valid && publication(p, "website").Status == "published" {
			return []platform.Task{{Type: TaskRevalidate, Payload: revalidatePayload{Slug: v.Slug.String}}}
		}
	}
	return nil
}

func publication(p Post, channel string) db.PostPublication {
	for _, pub := range p.Publications {
		if pub.Channel == channel {
			return pub
		}
	}
	return db.PostPublication{}
}

var errNotDue = errors.New("content: post not due")

// PublishScheduled starts publishing post id if it is still scheduled and
// its time has come; otherwise it changes nothing and returns no post.
func PublishScheduled(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) (Post, error) {
	p, err := change(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if p.Status != "scheduled" || p.ScheduledAt.Time.After(now) {
			return errNotDue
		}
		return startPublishing(ctx, q, p, now)
	})
	if errors.Is(err, errNotDue) || errors.Is(err, errPostNotFound) {
		return Post{}, nil
	}
	return p, err
}

// PublishChannel makes one attempt at posting channel of post id through
// publish. It returns an error only when the attempt failed and should be
// retried; a permanent failure, or the last attempt, leaves the channel
// failed for Daw Mi to retry or post by hand.
func PublishChannel(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string, publish Publisher,
	now time.Time) error {
	q := db.New(pool)
	pub, err := q.ClaimPostPublication(ctx, db.ClaimPostPublicationParams{PostID: id, Channel: channel, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // done, taken by another worker, or the post moved on
	}
	if err != nil {
		return err
	}
	versions, err := q.ListPostVersions(ctx, id)
	if err != nil {
		return err
	}
	var version db.PostVersion
	for _, v := range versions {
		if v.Channel == channel {
			version = v
		}
	}

	posted, sendErr := publish(ctx, version)
	result := db.FinishPostPublicationParams{PostID: id, Channel: channel, Status: "published", Now: now}
	var retry error
	if sendErr == nil {
		result.ExternalID = pgtype.Text{String: posted.ExternalID, Valid: posted.ExternalID != ""}
		result.Permalink = pgtype.Text{String: posted.Permalink, Valid: posted.Permalink != ""}
		result.PublishedAt = sql.NullTime{Time: now, Valid: true}
	} else {
		ce := &ChannelError{Reason: "connection_failed", Transient: true}
		errors.As(sendErr, &ce)
		result.Status, result.Error = "failed", pgtype.Text{String: ce.Reason, Valid: true}
		if ce.Transient && pub.Attempts < maxAttempts {
			result.Status, retry = "pending", sendErr
		}
	}
	_, err = change(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if _, err := q.FinishPostPublication(ctx, result); err != nil {
			return err
		}
		if p.Status != "publishing" {
			return nil // sent back to review meanwhile; publishing it again settles it
		}
		return settle(ctx, q, p, now)
	})
	return cmp.Or(err, retry)
}

var errNotFailed = apperr.New(apperr.ActionNotAllowed, "Only a channel that failed can be retried.")

// Retry gives a failed social channel a fresh set of attempts.
func Retry(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string, now time.Time) (Post, error) {
	return change(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "retried", "publishing"); err != nil {
			return err
		}
		_, err := q.RetryPostPublication(ctx, db.RetryPostPublicationParams{PostID: id, Channel: channel, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFailed
		}
		return err
	})
}

// DueTasks rebuilds the tasks Redis lost (ADR-006): scheduled posts past
// their time, and social channels waiting for longer than stuckAfter. Task
// ids match the ones first given, so a task still queued is not added
// twice.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]platform.Task, error) {
	posts, err := q.ListDueScheduledPosts(ctx, db.ListDueScheduledPostsParams{
		Before: now.Add(-scheduleGrace), MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}
	pubs, err := q.ListLostPostPublications(ctx, db.ListLostPostPublicationsParams{
		Before: now.Add(-stuckAfter), MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}
	var tasks []platform.Task
	for _, p := range posts {
		tasks = append(tasks, scheduledTask(p.ID, p.ScheduledAt.Time))
	}
	for _, pub := range pubs {
		tasks = append(tasks, channelTask(pub))
	}
	return tasks, nil
}

// FailStuck fails as unknown_outcome every channel whose attempt has run for
// longer than stuckAfter: its worker died mid-call, so the post may be on
// the platform already. Sending it again could post it twice, so Daw Mi
// checks the platform and then retries or marks it posted.
func FailStuck(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.FailStuckPostPublications(ctx, db.FailStuckPostPublicationsParams{Before: now.Add(-stuckAfter), Now: now})
}
