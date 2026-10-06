// Package content is the publishing portal (ADR-009): posts, a version of
// each post for every channel, the workflow from idea to published, and the
// website articles the site reads.
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

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// Post is a post with its channel versions and how each channel's
// publishing went.
type Post struct {
	db.Post
	Versions     []db.PostVersion
	Publications []db.PostPublication
}

// Edit is an author's change to a post. An empty Title, Kind or Status and a
// nil Consent leave that field as it is; each of Versions replaces that
// channel's version whole.
type Edit struct {
	Title    string
	Kind     string
	Status   string // idea or draft
	Consent  *Consent
	Versions []db.SavePostVersionParams
}

// Consent is the author's confirmation that a True Story's storyteller
// agreed to the post, and where that agreement is kept.
type Consent struct {
	Confirmed bool
	Note      string
}

var (
	errPostNotFound = apperr.New(apperr.NotFound, "No post has this id.")
	errStalePost    = apperr.New(apperr.StaleVersion, "The post changed since it was read; reload it.")
	errSlugTaken    = apperr.New(apperr.SlugTaken, "Another article uses this slug.")
)

// GetPost reads a post with its versions and publications.
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

// PostFilter narrows the post list; empty fields do not filter.
type PostFilter struct {
	Status, Search, Cursor string
	Limit                  int
}

// PostPage is one page of posts, newest first.
type PostPage struct {
	Items      []db.ListPostsRow
	NextCursor string
}

// ListPosts lists posts newest first, with the channels each has enabled.
func ListPosts(ctx context.Context, q db.Querier, f PostFilter) (PostPage, error) {
	limit := cmp.Or(f.Limit, 50)
	p := db.ListPostsParams{
		Status:  pgtype.Text{String: f.Status, Valid: f.Status != ""},
		Search:  platform.LikePattern(f.Search),
		MaxRows: int32(limit) + 1,
	}
	if f.Cursor != "" {
		var err error
		if p.AfterAt, p.AfterID, err = platform.DecodeCursor(f.Cursor); err != nil {
			return PostPage{}, err
		}
	}
	rows, err := q.ListPosts(ctx, p)
	if err != nil {
		return PostPage{}, err
	}
	page := PostPage{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = platform.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// CreatePost saves a new post by author, a draft unless e makes it an idea.
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
	return out, refusal(err)
}

// UpdatePost applies e to the post as of version. Saving an approved or
// scheduled post sends it back to review, so nothing unreviewed goes out;
// once publishing has started the post can no longer be edited.
func UpdatePost(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, e Edit,
	actor pgtype.UUID, now time.Time) (Post, error) {
	return change(ctx, pool, id, version, now, func(q *db.Queries, p *db.Post) error {
		if e.Status != "" {
			if err := requireStatus(p, "moved to "+e.Status, "idea", "draft"); err != nil {
				return err
			}
			p.Status = e.Status
		}
		switch p.Status {
		case "idea", "draft", "in_review":
		case "approved", "scheduled":
			backToReview(p)
		default:
			return apperr.New(apperr.InvalidTransition, "A post that is "+p.Status+" cannot be edited.")
		}
		p.Title = cmp.Or(e.Title, p.Title)
		p.Kind = cmp.Or(e.Kind, p.Kind)
		setConsent(p, e.Consent, actor, now)
		return saveVersions(ctx, q, p.ID, e.Versions, now)
	})
}

// DeletePost deletes an idea or a draft; anything further along is
// archived instead, so what was reviewed or published keeps its record.
func DeletePost(ctx context.Context, q db.Querier, id pgtype.UUID) error {
	n, err := q.DeletePost(ctx, id)
	if err != nil || n > 0 {
		return err
	}
	if _, err := GetPost(ctx, q, id); err != nil {
		return err
	}
	return apperr.New(apperr.InvalidTransition, "Only ideas and drafts can be deleted; archive the post instead.")
}

// setConsent records who confirmed consent and when; confirming again keeps
// the first confirmation.
func setConsent(p *db.Post, c *Consent, by pgtype.UUID, now time.Time) {
	if c == nil {
		return
	}
	p.ConsentNote = pgtype.Text{String: c.Note, Valid: c.Note != ""}
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

// anyVersion skips the version check, for changes that cannot overwrite
// what the caller last read, such as marking one channel posted.
const anyVersion = 0

// change applies fn to the post as of version, holding the post's lock
// until the change is saved, and returns the post as saved.
func change(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, now time.Time,
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
	return out, refusal(err)
}

// refusal names the one constraint an author's input can break; the unique
// index is the guard, so two drafts racing for a slug cannot both win.
func refusal(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "post_versions_slug_key" {
		return errSlugTaken
	}
	return err
}
