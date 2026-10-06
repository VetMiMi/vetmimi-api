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

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// The workflow (docs/data-model.md, "Posts"): idea ⇄ draft → in_review →
// approved → scheduled → publishing → published. A request for changes
// sends in_review back to draft, an edit sends approved or scheduled back to
// in_review, and any status may be archived. Each action below names the
// statuses it starts from; the HTTP role table decides who may take it.

// Submit puts an idea or a draft in front of the reviewer.
func Submit(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "submitted", "idea", "draft"); err != nil {
			return err
		}
		p.Status = "in_review"
		return nil
	})
}

// RequestChanges sends a post in review back to its author with a note.
func RequestChanges(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, note string, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "sent back", "in_review"); err != nil {
			return err
		}
		p.Status = "draft"
		p.ReviewNote = pgtype.Text{String: note, Valid: true}
		return nil
	})
}

// Approve approves a post in review once every enabled channel version is
// ready for its platform and a True Story's consent is confirmed.
func Approve(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, by pgtype.UUID, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "approved", "in_review"); err != nil {
			return err
		}
		versions, err := q.ListPostVersions(ctx, p.ID)
		if err != nil {
			return err
		}
		if problems := checkApproval(*p, versions); len(problems) > 0 {
			return &apperr.Error{Code: apperr.PublishRequirementsUnmet,
				Detail: "The post is not ready to approve.", Fields: problems}
		}
		p.Status = "approved"
		p.ApprovedAt, p.ApprovedBy = sql.NullTime{Time: now, Valid: true}, by
		return nil
	})
}

// Schedule sets an approved post, or moves a scheduled one, to go out at a
// future time.
func Schedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, at, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "scheduled", "approved", "scheduled"); err != nil {
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

// Unschedule takes a scheduled post back to approved.
func Unschedule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "unscheduled", "scheduled"); err != nil {
			return err
		}
		p.Status = "approved"
		p.ScheduledAt = sql.NullTime{}
		return nil
	})
}

// PublishNow publishes an approved or scheduled post at once.
func PublishNow(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "published", "approved", "scheduled"); err != nil {
			return err
		}
		return startPublishing(ctx, q, p, now)
	})
}

// Archive takes a post out of the workflow; a published article leaves the
// website. Its record and publications stay.
func Archive(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(_ *db.Queries, p *db.Post) error {
		if p.Status == "archived" {
			return errTransition(p, "archived")
		}
		p.Status = "archived"
		p.ScheduledAt = sql.NullTime{}
		return nil
	})
}

var errNotWaiting = apperr.New(apperr.ActionNotAllowed, "This channel is not waiting to be posted by hand.")

// MarkPosted records that Daw Mi posted a social channel herself (copy &
// open), with the post's address when she has it.
func MarkPosted(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, channel, permalink string, now time.Time) (Post, error) {
	return change(ctx, pool, id, anyVersion, now, func(q *db.Queries, p *db.Post) error {
		if err := requireStatus(p, "marked posted", "publishing", "published"); err != nil {
			return err
		}
		_, err := q.MarkPublicationManual(ctx, db.MarkPublicationManualParams{
			PostID: p.ID, Channel: channel, Now: now,
			Permalink: pgtype.Text{String: permalink, Valid: permalink != ""},
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

// startPublishing opens a publication for every enabled channel. The website
// goes live at once; social channels wait as pending for the publishing
// worker, or for Daw Mi to post them by hand. The scheduled-publishing task
// is to call this too, for a scheduled post whose time has come, inside the
// same change so the post's lock covers it.
func startPublishing(ctx context.Context, q *db.Queries, p *db.Post, now time.Time) error {
	versions, err := q.ListPostVersions(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, v := range versions {
		if !v.Enabled {
			continue
		}
		pub := db.CreatePostPublicationParams{PostID: p.ID, Channel: v.Channel, Status: "pending", Now: now}
		if v.Channel == "website" {
			pub.Status, pub.PublishedAt = "published", sql.NullTime{Time: now, Valid: true}
		}
		if err := q.CreatePostPublication(ctx, pub); err != nil {
			return err
		}
	}
	return settle(ctx, q, p, now)
}

// settle makes the post published once every channel it opened is
// published or posted by hand, and publishing until then.
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

// backToReview withdraws an approval, and any schedule with it.
func backToReview(p *db.Post) {
	p.Status = "in_review"
	p.ScheduledAt = sql.NullTime{}
	p.ApprovedAt, p.ApprovedBy = sql.NullTime{}, pgtype.UUID{}
}

func requireStatus(p *db.Post, action string, from ...string) error {
	if slices.Contains(from, p.Status) {
		return nil
	}
	return errTransition(p, action)
}

func errTransition(p *db.Post, action string) error {
	return apperr.New(apperr.InvalidTransition, "A post that is "+p.Status+" cannot be "+action+".")
}
