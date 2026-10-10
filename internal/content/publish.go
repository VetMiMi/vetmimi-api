package content

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	maxAttempts   = 4                // a first try and three retries, spaced out by asynq
	stuckAfter    = 10 * time.Minute // far longer than any attempt; then the sweep steps in
	scheduleGrace = time.Minute      // a post just due is left to its own task
	sweepLimit    = 100
)

type Posted struct {
	ExternalID, Permalink string
}

type Publisher func(ctx context.Context, v db.PostVersion) (Posted, error)

// ChannelError's Reason is the code the portal shows; a transient one is tried again.
type ChannelError struct {
	Reason    string
	Transient bool
}

func (e *ChannelError) Error() string { return "content: channel not posted: " + e.Reason }

var ErrNotConnected = &ChannelError{Reason: "not_connected"}

// NotConnected is the Publisher of a channel Daw Mi posts by hand.
func NotConnected(context.Context, db.PostVersion) (Posted, error) { return Posted{}, ErrNotConnected }

type publishPayload struct {
	PostID  string `json:"postId"`
	Channel string `json:"channel,omitempty"`
}

type revalidatePayload struct {
	Slug string `json:"slug"`
}

// ScheduledTask is p's publish task; one left behind by a moved schedule does nothing.
func ScheduledTask(p Post) []queue.Task {
	if p.Status != "scheduled" {
		return nil
	}
	return []queue.Task{newScheduledTask(p.ID, p.ScheduledAt.Time)}
}

func PublishTasks(p Post) []queue.Task {
	var out []queue.Task
	for _, pub := range p.Publications {
		if pub.Status == "pending" {
			out = append(out, newChannelTask(pub))
		}
	}
	return out
}

// RevalidateTasks is empty until the website is live: an edit alone changes nothing visitors read.
func RevalidateTasks(p Post) []queue.Task {
	site := channelVersion(p.Versions, "website")
	if !site.Slug.Valid || publication(p, "website").Status != "published" {
		return nil
	}
	return []queue.Task{{Type: TaskRevalidate, Payload: revalidatePayload{Slug: site.Slug.String}}}
}

func newScheduledTask(id pgtype.UUID, at time.Time) queue.Task {
	return queue.Task{Type: TaskPublishScheduled, ID: fmt.Sprintf("post:%s:%d", id, at.Unix()),
		Payload: publishPayload{PostID: id.String()}, ProcessAt: at}
}

// The id names the attempt, so a retry is a new task but a duplicate is not added.
func newChannelTask(pub db.PostPublication) queue.Task {
	return queue.Task{Type: TaskPublishChannel,
		ID:      fmt.Sprintf("publish:%s:%s:%d", pub.PostID, pub.Channel, pub.Attempts),
		Payload: publishPayload{PostID: pub.PostID.String(), Channel: pub.Channel}}
}

var errNotDue = errors.New("content: post not due")

// PublishScheduled returns no post when the post is gone, no longer scheduled or not yet due.
func PublishScheduled(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) (Post, error) {
	p, err := changePost(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
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

// PublishChannel makes one attempt and returns an error only when asynq should try again.
func PublishChannel(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string, publish Publisher,
	now time.Time) error {
	q := db.New(pool)
	// A channel already publishing is never claimed again: its attempt may have posted.
	pub, err := q.ClaimPostPublication(ctx, db.ClaimPostPublicationParams{PostID: id, Channel: channel, Now: now})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	versions, err := q.ListPostVersions(ctx, id)
	if err != nil {
		return err
	}
	posted, sendErr := publish(ctx, channelVersion(versions, channel))
	result, retryErr := attemptResult(pub, posted, sendErr, now)
	if err := recordAttempt(ctx, pool, result, now); err != nil {
		return err
	}
	return retryErr
}

// attemptResult returns the error asynq should retry on, or nil.
func attemptResult(pub db.PostPublication, posted Posted, sendErr error, now time.Time) (db.FinishPostPublicationParams, error) {
	result := db.FinishPostPublicationParams{PostID: pub.PostID, Channel: pub.Channel, Now: now}
	if sendErr == nil {
		result.Status = "published"
		result.ExternalID = optionalText(posted.ExternalID)
		result.Permalink = optionalText(posted.Permalink)
		result.PublishedAt = sql.NullTime{Time: now, Valid: true}
		return result, nil
	}
	// An error that is not a ChannelError counts as a transient connection failure.
	ce := &ChannelError{Reason: "connection_failed", Transient: true}
	errors.As(sendErr, &ce)
	result.Error = pgtype.Text{String: ce.Reason, Valid: true}
	if ce.Transient && pub.Attempts < maxAttempts {
		result.Status = "pending"
		return result, sendErr
	}
	result.Status = "failed"
	return result, nil
}

func recordAttempt(ctx context.Context, pool *pgxpool.Pool, result db.FinishPostPublicationParams, now time.Time) error {
	_, err := changePost(ctx, pool, result.PostID, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if _, err := q.FinishPostPublication(ctx, result); err != nil {
			return err
		}
		// Sent back to review meanwhile: publishing it again settles it.
		if p.Status != "publishing" {
			return nil
		}
		return settle(ctx, q, p, now)
	})
	return err
}

// DueTasks rebuilds the tasks Redis lost, with their first ids so none is added twice.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]queue.Task, error) {
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
	var tasks []queue.Task
	for _, p := range posts {
		tasks = append(tasks, newScheduledTask(p.ID, p.ScheduledAt.Time))
	}
	for _, pub := range pubs {
		tasks = append(tasks, newChannelTask(pub))
	}
	return tasks, nil
}

// FailStuck marks long-running attempts unknown_outcome and never re-sends them:
// the worker died mid-call, so the post may already be on the platform.
func FailStuck(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	return q.FailStuckPostPublications(ctx, db.FailStuckPostPublicationsParams{Before: now.Add(-stuckAfter), Now: now})
}

func channelVersion(versions []db.PostVersion, channel string) db.PostVersion {
	for _, v := range versions {
		if v.Channel == channel {
			return v
		}
	}
	return db.PostVersion{}
}

func publication(p Post, channel string) db.PostPublication {
	for _, pub := range p.Publications {
		if pub.Channel == channel {
			return pub
		}
	}
	return db.PostPublication{}
}
