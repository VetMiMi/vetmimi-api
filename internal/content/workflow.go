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

type action struct {
	name string   // as in "cannot be <name>"
	from []string // the statuses it may start from
}

// idea ⇄ draft → in_review → approved → scheduled → publishing → published;
// an edit sends approved and later posts back to in_review (editStatus).
var (
	submit         = action{"submitted", []string{"idea", "draft"}}
	requestChanges = action{"sent back", []string{"in_review"}}
	approve        = action{"approved", []string{"in_review"}}
	schedule       = action{"scheduled", []string{"approved", "scheduled"}}
	unschedule     = action{"unscheduled", []string{"scheduled"}}
	publishNow     = action{"published", []string{"approved", "scheduled"}}
	markPosted     = action{"marked posted", []string{"publishing", "published"}}
	retry          = action{"retried", []string{"publishing"}}
	archive        = action{"archived", []string{"idea", "draft", "in_review", "approved", "scheduled", "publishing", "published"}}
)

func (a action) check(p *db.Post) error {
	if slices.Contains(a.from, p.Status) {
		return nil
	}
	return errTransition(p, a.name)
}

func errTransition(p *db.Post, name string) error {
	return apperr.New(apperr.InvalidTransition, "A post that is "+p.Status+" cannot be "+name+".")
}

var (
	errNotWaiting = apperr.New(apperr.ActionNotAllowed, "This channel is not waiting to be posted by hand.")
	errNotFailed  = apperr.New(apperr.ActionNotAllowed, "Only a channel that failed can be retried.")
)

func Submit(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := submit.check(p); err != nil {
			return err
		}
		p.Status = "in_review"
		return nil
	})
}

func RequestChanges(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, note string, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := requestChanges.check(p); err != nil {
			return err
		}
		p.Status = "draft"
		p.ReviewNote = pgtype.Text{String: note, Valid: true}
		return nil
	})
}

func Approve(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, by pgtype.UUID, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if err := approve.check(p); err != nil {
			return err
		}
		versions, err := q.ListPostVersions(ctx, p.ID)
		if err != nil {
			return err
		}
		inLibrary, err := mediaInLibrary(ctx, q, versions)
		if err != nil {
			return err
		}
		if problems := checkApproval(*p, versions, inLibrary); len(problems) > 0 {
			return &apperr.Error{Code: apperr.PublishRequirementsUnmet,
				Detail: "The post is not ready to approve.", Fields: problems}
		}
		p.Status = "approved"
		p.ApprovedAt, p.ApprovedBy = sql.NullTime{Time: now, Valid: true}, by
		return nil
	})
}

func Schedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, at, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := schedule.check(p); err != nil {
			return err
		}
		if !at.After(now) {
			return &apperr.Error{Code: apperr.ActionNotAllowed, Detail: "A post is scheduled for the future.",
				Fields: []apperr.FieldError{{Field: "/scheduledAt", Message: "must be in the future"}}}
		}
		p.Status = "scheduled"
		p.ScheduledAt = sql.NullTime{Time: at, Valid: true}
		return nil
	})
}

func Unschedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := unschedule.check(p); err != nil {
			return err
		}
		p.Status = "approved"
		p.ScheduledAt = sql.NullTime{}
		return nil
	})
}

func PublishNow(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if err := publishNow.check(p); err != nil {
			return err
		}
		return startPublishing(ctx, q, p, now)
	})
}

// Archive takes a published article off the website but keeps its record.
func Archive(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := archive.check(p); err != nil {
			return err
		}
		p.Status = "archived"
		p.ScheduledAt = sql.NullTime{}
		return nil
	})
}

// MarkPosted records that Daw Mi posted a social channel by hand.
func MarkPosted(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel, permalink string, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if err := markPosted.check(p); err != nil {
			return err
		}
		_, err := q.MarkPublicationManual(ctx, db.MarkPublicationManualParams{
			PostID: p.ID, Channel: channel, Permalink: optionalText(permalink), Now: now,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotWaiting
		}
		if err != nil {
			return err
		}
		return settle(ctx, q, p, now)
	})
}

func Retry(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel string, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if err := retry.check(p); err != nil {
			return err
		}
		_, err := q.RetryPostPublication(ctx, db.RetryPostPublicationParams{PostID: id, Channel: channel, Now: now})
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFailed
		}
		return err
	})
}

// startPublishing puts the website live at once and leaves social channels pending.
func startPublishing(ctx context.Context, q *db.Queries, p *db.Post, now time.Time) error {
	versions, err := q.ListPostVersions(ctx, p.ID)
	if err != nil {
		return err
	}
	// Delete the old copy first, so a post whose website was switched off leaves the site.
	if err := q.DeleteArticle(ctx, p.ID); err != nil {
		return err
	}
	for _, v := range versions {
		if !v.Enabled {
			continue
		}
		pub := db.OpenPostPublicationParams{PostID: p.ID, Channel: v.Channel, Status: "pending", Now: now}
		if v.Channel == "website" {
			pub.Status, pub.PublishedAt = "published", sql.NullTime{Time: now, Valid: true}
			if err := q.SnapshotArticle(ctx, db.SnapshotArticleParams{PostID: p.ID, Now: now}); err != nil {
				return err
			}
		}
		// A social channel opened before is left as it is, so publishing again never re-sends it.
		if err := q.OpenPostPublication(ctx, pub); err != nil {
			return err
		}
	}
	return settle(ctx, q, p, now)
}

// settle marks the post published once every channel is published or posted by hand.
func settle(ctx context.Context, q *db.Queries, p *db.Post, now time.Time) error {
	pubs, err := q.ListPostPublications(ctx, p.ID)
	if err != nil {
		return err
	}
	p.Status = "published"
	for _, pub := range pubs {
		if pub.Status != "published" && pub.Status != "manual" {
			p.Status = "publishing"
		}
	}
	if p.Status == "published" && !p.PublishedAt.Valid {
		p.PublishedAt = sql.NullTime{Time: now, Valid: true}
	}
	return nil
}

func backToReview(p *db.Post) {
	p.Status = "in_review"
	p.ScheduledAt = sql.NullTime{}
	p.ApprovedAt, p.ApprovedBy = sql.NullTime{}, pgtype.UUID{}
}
