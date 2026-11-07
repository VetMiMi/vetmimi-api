package content

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// action is a workflow step and the statuses it may start from.
type action struct {
	name string // as in "cannot be <name>"
	from []string
}

// idea ⇄ draft → scheduled → publishing → published, or straight to publishing.
// in_review and approved remain only on posts saved before the review step was removed.
var (
	unpublished = []string{"idea", "draft", "in_review", "approved"}
	schedule    = action{name: "scheduled", from: append(unpublished, "scheduled")}
	unschedule  = action{name: "unscheduled", from: []string{"scheduled"}}
	publishNow  = action{name: "published", from: append(unpublished, "scheduled")}
	markPosted  = action{name: "marked posted", from: []string{"publishing", "published"}}
	retry       = action{name: "retried", from: []string{"publishing"}}
	archive     = action{name: "archived", from: append(unpublished, "scheduled", "publishing", "published")}
)

var (
	errNotWaiting  = apperr.New(apperr.ActionNotAllowed, "This channel is not waiting to be posted by hand.")
	errNotFailed   = apperr.New(apperr.ActionNotAllowed, "Only a channel that failed can be retried.")
	errNotInFuture = &apperr.Error{
		Code:   apperr.ActionNotAllowed,
		Detail: "A post is scheduled for the future.",
		Fields: []apperr.FieldError{{Field: "/scheduledAt", Message: "must be in the future"}},
	}
)

func Schedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, at time.Time,
	by pgtype.UUID, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, version)
	if err != nil {
		return Post{}, err
	}
	err = schedule.check(&post)
	if err != nil {
		return Post{}, err
	}
	if !at.After(now) {
		return Post{}, errNotInFuture
	}
	err = checkReady(ctx, q, &post, by, now)
	if err != nil {
		return Post{}, err
	}
	post.Status = "scheduled"
	post.ScheduledAt = sql.NullTime{Time: at, Valid: true}
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

func Unschedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, version)
	if err != nil {
		return Post{}, err
	}
	err = unschedule.check(&post)
	if err != nil {
		return Post{}, err
	}
	backToDraft(&post)
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

func PublishNow(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, by pgtype.UUID,
	now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, version)
	if err != nil {
		return Post{}, err
	}
	err = publishNow.check(&post)
	if err != nil {
		return Post{}, err
	}
	err = checkReady(ctx, q, &post, by, now)
	if err != nil {
		return Post{}, err
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

// Archive takes a published article off the website but keeps its record.
func Archive(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, version)
	if err != nil {
		return Post{}, err
	}
	err = archive.check(&post)
	if err != nil {
		return Post{}, err
	}
	post.Status = "archived"
	post.ScheduledAt = sql.NullTime{}
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

// MarkPosted records that Daw Mi posted a social channel by hand.
func MarkPosted(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel, permalink string,
	now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, anyVersion)
	if err != nil {
		return Post{}, err
	}
	err = markPosted.check(&post)
	if err != nil {
		return Post{}, err
	}
	_, err = q.MarkPublicationManual(ctx, db.MarkPublicationManualParams{
		PostID:    post.ID,
		Channel:   channel,
		Permalink: optionalText(permalink),
		Now:       now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, errNotWaiting
	}
	if err != nil {
		return Post{}, err
	}
	err = settle(ctx, q, &post, now)
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

func Retry(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post, err := lockPost(ctx, q, id, anyVersion)
	if err != nil {
		return Post{}, err
	}
	err = retry.check(&post)
	if err != nil {
		return Post{}, err
	}
	_, err = q.RetryPostPublication(ctx, db.RetryPostPublicationParams{
		PostID:  post.ID,
		Channel: channel,
		Now:     now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, errNotFailed
	}
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

func (a action) check(post *db.Post) error {
	if slices.Contains(a.from, post.Status) {
		return nil
	}
	return errTransition(post, a.name)
}

func errTransition(post *db.Post, name string) error {
	return apperr.New(apperr.InvalidTransition, "A post that is "+post.Status+" cannot be "+name+".")
}

// startPublishing puts the website live at once and leaves social channels pending.
func startPublishing(ctx context.Context, q *db.Queries, post *db.Post, now time.Time) error {
	versions, err := q.ListPostVersions(ctx, post.ID)
	if err != nil {
		return err
	}
	// Delete the old copy first, so a post whose website was switched off leaves the site.
	err = q.DeleteArticle(ctx, post.ID)
	if err != nil {
		return err
	}

	for _, version := range versions {
		if !version.Enabled {
			continue
		}
		publication := db.OpenPostPublicationParams{
			PostID:  post.ID,
			Channel: version.Channel,
			Status:  "pending",
			Now:     now,
		}
		if version.Channel == "website" {
			publication.Status = "published"
			publication.PublishedAt = sql.NullTime{Time: now, Valid: true}
			err = q.SnapshotArticle(ctx, db.SnapshotArticleParams{PostID: post.ID, Now: now})
			if err != nil {
				return asSlugTaken(err)
			}
		}
		// A social channel opened before is left as it is, so publishing again never re-sends it.
		err = q.OpenPostPublication(ctx, publication)
		if err != nil {
			return err
		}
	}
	return settle(ctx, q, post, now)
}

// settle marks the post published once every channel is published or posted by hand.
func settle(ctx context.Context, q *db.Queries, post *db.Post, now time.Time) error {
	publications, err := q.ListPostPublications(ctx, post.ID)
	if err != nil {
		return err
	}
	post.Status = "published"
	for _, publication := range publications {
		if publication.Status != "published" && publication.Status != "manual" {
			post.Status = "publishing"
		}
	}
	if post.Status == "published" && !post.PublishedAt.Valid {
		post.PublishedAt = sql.NullTime{Time: now, Valid: true}
	}
	return nil
}

func backToDraft(post *db.Post) {
	post.Status = "draft"
	post.ScheduledAt = sql.NullTime{}
	post.ApprovedAt = sql.NullTime{}
	post.ApprovedBy = pgtype.UUID{}
}
