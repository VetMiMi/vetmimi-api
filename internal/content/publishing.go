package content

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

const maxAttempts = 4 // a first try and three retries, spaced out by asynq

// Publisher posts one channel's version to its platform.
type Publisher func(ctx context.Context, version db.PostVersion) (Posted, error)

type Posted struct {
	ExternalID string
	Permalink  string
}

// ChannelError's Reason is the code the portal shows; a transient one is tried again.
type ChannelError struct {
	Reason    string
	Transient bool
}

func (failure *ChannelError) Error() string {
	return "content: channel not posted: " + failure.Reason
}

var ErrNotConnected = &ChannelError{Reason: "not_connected"}

// NotConnected is the Publisher of a channel Daw Mi posts by hand.
func NotConnected(context.Context, db.PostVersion) (Posted, error) {
	return Posted{}, ErrNotConnected
}

// publishDuePost starts publishing a scheduled post whose time has come.
// A post that is gone, no longer scheduled or not yet due is left alone.
func publishDuePost(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, anyVersion)
	if errors.Is(err, errPostNotFound) {
		return Post{}, nil
	}
	if err != nil {
		return Post{}, err
	}
	if post.Status != "scheduled" || post.ScheduledAt.Time.After(now) {
		return Post{}, nil
	}
	err = startPublishing(ctx, q, &post, now)
	if err != nil {
		return Post{}, err
	}
	result, err := savePost(ctx, q, post, now)
	if err != nil {
		return Post{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Post{}, err
	}
	return result, nil
}

// publishToChannel makes one attempt and returns an error only when asynq should try again.
func publishToChannel(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string,
	publish Publisher, now time.Time) error {
	q := db.New(pool)

	// A channel already publishing is never claimed again: its attempt may have posted.
	publication, err := q.ClaimPostPublication(ctx, db.ClaimPostPublicationParams{
		PostID:  id,
		Channel: channel,
		Now:     now,
	})
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
	result, retryErr := attemptResult(publication, posted, sendErr, now)
	err = recordAttempt(ctx, pool, result, now)
	if err != nil {
		return err
	}
	return retryErr
}

// attemptResult is the row to save, and the error asynq should retry on, or nil.
func attemptResult(publication db.PostPublication, posted Posted, sendErr error,
	now time.Time) (db.FinishPostPublicationParams, error) {
	result := db.FinishPostPublicationParams{
		PostID:  publication.PostID,
		Channel: publication.Channel,
		Now:     now,
	}
	if sendErr == nil {
		result.Status = "published"
		result.ExternalID = optionalText(posted.ExternalID)
		result.Permalink = optionalText(posted.Permalink)
		result.PublishedAt = sql.NullTime{Time: now, Valid: true}
		return result, nil
	}

	// An error that is not a ChannelError counts as a transient connection failure.
	channelErr := &ChannelError{Reason: "connection_failed", Transient: true}
	var known *ChannelError
	if errors.As(sendErr, &known) {
		channelErr = known
	}
	result.Error = pgtype.Text{String: channelErr.Reason, Valid: true}
	if channelErr.Transient && publication.Attempts < maxAttempts {
		result.Status = "pending"
		return result, sendErr
	}
	result.Status = "failed"
	return result, nil
}

func recordAttempt(ctx context.Context, pool *pgxpool.Pool, result db.FinishPostPublicationParams,
	now time.Time) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, result.PostID, anyVersion)
	if err != nil {
		return err
	}
	_, err = q.FinishPostPublication(ctx, result)
	if err != nil {
		return err
	}
	// Sent back to draft meanwhile: publishing it again settles it.
	if post.Status == "publishing" {
		err = settle(ctx, q, &post, now)
		if err != nil {
			return err
		}
	}
	_, err = savePost(ctx, q, post, now)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func channelVersion(versions []db.PostVersion, channel string) db.PostVersion {
	for _, version := range versions {
		if version.Channel == channel {
			return version
		}
	}
	return db.PostVersion{}
}
