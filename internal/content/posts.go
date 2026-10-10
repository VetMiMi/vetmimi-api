// Package content is the publishing portal: posts, a version of each post per
// channel, and the workflow from idea to published. Publishing puts the website
// article live at once and leaves each social channel to the worker (Tasks).
package content

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
)

type Post struct {
	db.Post
	Versions     []db.PostVersion
	Publications []db.PostPublication
}

// Edit leaves empty fields and a nil Consent as they are; each version is replaced whole.
type Edit struct {
	Title    string
	Kind     string
	Status   string // idea or draft
	Consent  *Consent
	Versions []db.SavePostVersionParams
}

// Consent is a True Story storyteller's agreement; Note says where it is kept.
type Consent struct {
	Confirmed bool
	Note      string
}

type PostFilter struct {
	Status, Search, Cursor string
	Limit                  int
}

type PostPage struct {
	Items      []db.ListPostsRow
	NextCursor string
}

// anyVersion skips the version check, for changes that cannot overwrite what the caller read.
const anyVersion = 0

var (
	errPostNotFound = apperr.New(apperr.NotFound, "No post has this id.")
	errStalePost    = apperr.New(apperr.StaleVersion, "The post changed since it was read; reload it.")
	errSlugTaken    = apperr.New(apperr.SlugTaken, "Another article uses this slug.")
	errLiveEdit     = apperr.New(apperr.ActionNotAllowed,
		"Once a post has gone out, only its title and website version can be edited.")
	errNotDeletable = apperr.New(apperr.InvalidTransition,
		"Only ideas and drafts that never went out can be deleted; archive the post instead.")
)

func GetPost(ctx context.Context, q db.Querier, id pgtype.UUID) (Post, error) {
	p, err := q.GetPost(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, errPostNotFound
	}
	if err != nil {
		return Post{}, err
	}
	return withChannels(ctx, q, p)
}

func ListPosts(ctx context.Context, q db.Querier, f PostFilter) (PostPage, error) {
	limit := cmp.Or(f.Limit, 50)
	params := db.ListPostsParams{
		Status:  optionalText(f.Status),
		Search:  listing.LikePattern(f.Search),
		MaxRows: int32(limit) + 1,
	}
	if f.Cursor != "" {
		var err error
		if params.AfterAt, params.AfterID, err = listing.DecodeCursor(f.Cursor); err != nil {
			return PostPage{}, err
		}
	}
	rows, err := q.ListPosts(ctx, params)
	if err != nil {
		return PostPage{}, err
	}
	if len(rows) <= limit {
		return PostPage{Items: rows}, nil
	}
	last := rows[limit-1]
	return PostPage{Items: rows[:limit], NextCursor: listing.EncodeCursor(last.CreatedAt, last.ID)}, nil
}

func CreatePost(ctx context.Context, pool *pgxpool.Pool, e Edit, author pgtype.UUID, now time.Time) (Post, error) {
	var out Post
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		p := db.Post{Title: e.Title, Kind: e.Kind, Status: cmp.Or(e.Status, "draft")}
		setConsent(&p, e.Consent, author, now)
		created, err := q.CreatePost(ctx, db.CreatePostParams{
			Title: p.Title, Kind: p.Kind, Status: p.Status, ConsentConfirmedAt: p.ConsentConfirmedAt,
			ConsentConfirmedBy: p.ConsentConfirmedBy, ConsentNote: p.ConsentNote, AuthorID: author, Now: now,
		})
		if err != nil {
			return err
		}
		if err := saveVersions(ctx, q, created.ID, e.Versions, now); err != nil {
			return err
		}
		out, err = withChannels(ctx, q, created)
		return err
	})
	return out, asSlugTaken(err)
}

func UpdatePost(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, e Edit,
	actor pgtype.UUID, now time.Time) (Post, error) {
	return changePost(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if err := editStatus(p, e); err != nil {
			return err
		}
		p.Title = cmp.Or(e.Title, p.Title)
		p.Kind = cmp.Or(e.Kind, p.Kind)
		setConsent(p, e.Consent, actor, now)
		return saveVersions(ctx, q, p.ID, e.Versions, now)
	})
}

// editStatus sends an approved or published post back to review, so nothing unreviewed goes out.
func editStatus(p *db.Post, e Edit) error {
	if e.Status != "" {
		move := action{"moved to " + e.Status, []string{"idea", "draft"}}
		if err := move.check(p); err != nil {
			return err
		}
		p.Status = e.Status
	}
	switch p.Status {
	case "idea", "draft", "in_review":
		return nil
	case "approved", "scheduled":
		backToReview(p)
		return nil
	case "publishing", "published":
		if !websiteOnly(e) {
			return errLiveEdit
		}
		backToReview(p)
		return nil
	}
	return errTransition(p, "edited")
}

func websiteOnly(e Edit) bool {
	if e.Kind != "" || e.Consent != nil {
		return false
	}
	for _, v := range e.Versions {
		if v.Channel != "website" {
			return false
		}
	}
	return true
}

// DeletePost deletes only ideas and drafts that never went out; the rest are archived.
func DeletePost(ctx context.Context, q db.Querier, id pgtype.UUID) error {
	n, err := q.DeletePost(ctx, id)
	if err != nil || n > 0 {
		return err
	}
	if _, err := GetPost(ctx, q, id); err != nil {
		return err
	}
	return errNotDeletable
}

// setConsent keeps the first confirmation when consent is confirmed again.
func setConsent(p *db.Post, c *Consent, by pgtype.UUID, now time.Time) {
	if c == nil {
		return
	}
	p.ConsentNote = optionalText(c.Note)
	switch {
	case !c.Confirmed:
		p.ConsentConfirmedAt, p.ConsentConfirmedBy = sql.NullTime{}, pgtype.UUID{}
	case !p.ConsentConfirmedAt.Valid:
		p.ConsentConfirmedAt, p.ConsentConfirmedBy = sql.NullTime{Time: now, Valid: true}, by
	}
}

func saveVersions(ctx context.Context, q *db.Queries, id pgtype.UUID, versions []db.SavePostVersionParams, now time.Time) error {
	for _, v := range versions {
		v.PostID, v.Now = id, now
		if v.ImageIds == nil {
			v.ImageIds = []pgtype.UUID{}
		}
		if err := q.SavePostVersion(ctx, v); err != nil {
			return err
		}
	}
	return nil
}

// changePost locks the post, checks version, applies fn and saves the result.
func changePost(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time,
	fn func(q *db.Queries, p *db.Post) error) (Post, error) {
	var out Post
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		p, err := q.LockPost(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errPostNotFound
		}
		if err != nil {
			return err
		}
		if version != anyVersion && p.Version != version {
			return errStalePost
		}
		if err := fn(q, &p); err != nil {
			return err
		}
		saved, err := q.SavePost(ctx, db.SavePostParams{
			ID: p.ID, Title: p.Title, Kind: p.Kind, Status: p.Status, ScheduledAt: p.ScheduledAt,
			ConsentConfirmedAt: p.ConsentConfirmedAt, ConsentConfirmedBy: p.ConsentConfirmedBy,
			ConsentNote: p.ConsentNote, ReviewNote: p.ReviewNote, ApprovedAt: p.ApprovedAt,
			ApprovedBy: p.ApprovedBy, PublishedAt: p.PublishedAt, Now: now,
		})
		if err != nil {
			return err
		}
		out, err = withChannels(ctx, q, saved)
		return err
	})
	return out, asSlugTaken(err)
}

func withChannels(ctx context.Context, q db.Querier, p db.Post) (Post, error) {
	versions, err := q.ListPostVersions(ctx, p.ID)
	if err != nil {
		return Post{}, err
	}
	pubs, err := q.ListPostPublications(ctx, p.ID)
	if err != nil {
		return Post{}, err
	}
	return Post{Post: p, Versions: versions, Publications: pubs}, nil
}

// asSlugTaken relies on the unique indexes, so two drafts racing for a slug cannot both win.
func asSlugTaken(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.ConstraintName == "post_versions_slug_key" ||
		pgErr.ConstraintName == "published_articles_slug_key") {
		return errSlugTaken
	}
	return err
}

func optionalText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
