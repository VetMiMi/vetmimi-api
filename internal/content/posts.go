// Package content is the publishing portal: posts, a version of each post per
// channel, and the workflow from idea to published.
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
	Status string
	Search string
	Cursor string
	Limit  int
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
	row, err := q.GetPost(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, errPostNotFound
	}
	if err != nil {
		return Post{}, err
	}
	return loadChannels(ctx, q, row)
}

func ListPosts(ctx context.Context, q db.Querier, filter PostFilter) (PostPage, error) {
	limit := cmp.Or(filter.Limit, 50)
	params := db.ListPostsParams{
		Status:  optionalText(filter.Status),
		Search:  listing.LikePattern(filter.Search),
		MaxRows: int32(limit) + 1,
	}
	if filter.Cursor != "" {
		afterAt, afterID, err := listing.DecodeCursor(filter.Cursor)
		if err != nil {
			return PostPage{}, err
		}
		params.AfterAt = afterAt
		params.AfterID = afterID
	}

	rows, err := q.ListPosts(ctx, params)
	if err != nil {
		return PostPage{}, err
	}
	if len(rows) <= limit {
		return PostPage{Items: rows}, nil
	}
	last := rows[limit-1]
	page := PostPage{
		Items:      rows[:limit],
		NextCursor: listing.EncodeCursor(last.CreatedAt, last.ID),
	}
	return page, nil
}

func CreatePost(ctx context.Context, pool *pgxpool.Pool, edit Edit, author pgtype.UUID, now time.Time) (Post, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Post{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	post := db.Post{Title: edit.Title, Kind: edit.Kind, Status: cmp.Or(edit.Status, "draft")}
	setConsent(&post, edit.Consent, author, now)
	created, err := q.CreatePost(ctx, db.CreatePostParams{
		Title:              post.Title,
		Kind:               post.Kind,
		Status:             post.Status,
		ConsentConfirmedAt: post.ConsentConfirmedAt,
		ConsentConfirmedBy: post.ConsentConfirmedBy,
		ConsentNote:        post.ConsentNote,
		AuthorID:           author,
		Now:                now,
	})
	if err != nil {
		return Post{}, err
	}
	err = saveVersions(ctx, q, created.ID, edit.Versions, now)
	if err != nil {
		return Post{}, err
	}
	result, err := loadChannels(ctx, q, created)
	if err != nil {
		return Post{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Post{}, err
	}
	return result, nil
}

func UpdatePost(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, version int32, edit Edit,
	actor pgtype.UUID, now time.Time) (Post, error) {
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
	err = editStatus(&post, edit)
	if err != nil {
		return Post{}, err
	}
	post.Title = cmp.Or(edit.Title, post.Title)
	post.Kind = cmp.Or(edit.Kind, post.Kind)
	setConsent(&post, edit.Consent, actor, now)
	err = saveVersions(ctx, q, post.ID, edit.Versions, now)
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

// DeletePost deletes only ideas and drafts that never went out; the rest are archived.
func DeletePost(ctx context.Context, q db.Querier, id pgtype.UUID) error {
	deleted, err := q.DeletePost(ctx, id)
	if err != nil {
		return err
	}
	if deleted > 0 {
		return nil
	}

	_, err = GetPost(ctx, q, id)
	if err != nil {
		return err
	}
	return errNotDeletable
}

// editStatus sends a scheduled or published post back to draft, so it is checked again before it goes out.
func editStatus(post *db.Post, edit Edit) error {
	if edit.Status != "" {
		move := action{name: "moved to " + edit.Status, from: []string{"idea", "draft"}}
		err := move.check(post)
		if err != nil {
			return err
		}
		post.Status = edit.Status
	}

	switch post.Status {
	case "idea", "draft", "in_review":
		return nil
	case "approved", "scheduled":
		backToDraft(post)
		return nil
	case "publishing", "published":
		if !editsWebsiteOnly(edit) {
			return errLiveEdit
		}
		backToDraft(post)
		return nil
	}
	return errTransition(post, "edited")
}

func editsWebsiteOnly(edit Edit) bool {
	if edit.Kind != "" || edit.Consent != nil {
		return false
	}
	for _, version := range edit.Versions {
		if version.Channel != "website" {
			return false
		}
	}
	return true
}

// setConsent keeps the first confirmation when consent is confirmed again.
func setConsent(post *db.Post, consent *Consent, by pgtype.UUID, now time.Time) {
	if consent == nil {
		return
	}
	post.ConsentNote = optionalText(consent.Note)
	if !consent.Confirmed {
		post.ConsentConfirmedAt = sql.NullTime{}
		post.ConsentConfirmedBy = pgtype.UUID{}
		return
	}
	if !post.ConsentConfirmedAt.Valid {
		post.ConsentConfirmedAt = sql.NullTime{Time: now, Valid: true}
		post.ConsentConfirmedBy = by
	}
}

func saveVersions(ctx context.Context, q *db.Queries, postID pgtype.UUID, versions []db.SavePostVersionParams,
	now time.Time) error {
	for _, version := range versions {
		version.PostID = postID
		version.Now = now
		if version.ImageIds == nil {
			version.ImageIds = []pgtype.UUID{}
		}
		err := q.SavePostVersion(ctx, version)
		if err != nil {
			return asSlugTaken(err)
		}
	}
	return nil
}

// lockPost reads the post for update inside tx and checks the caller read its current version.
func lockPost(ctx context.Context, q *db.Queries, id pgtype.UUID, version int32) (db.Post, error) {
	post, err := q.LockPost(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Post{}, errPostNotFound
	}
	if err != nil {
		return db.Post{}, err
	}
	if version != anyVersion && post.Version != version {
		return db.Post{}, errStalePost
	}
	return post, nil
}

// savePost writes the changed post and returns it with its channels.
func savePost(ctx context.Context, q *db.Queries, post db.Post, now time.Time) (Post, error) {
	saved, err := q.SavePost(ctx, db.SavePostParams{
		ID:                 post.ID,
		Title:              post.Title,
		Kind:               post.Kind,
		Status:             post.Status,
		ScheduledAt:        post.ScheduledAt,
		ConsentConfirmedAt: post.ConsentConfirmedAt,
		ConsentConfirmedBy: post.ConsentConfirmedBy,
		ConsentNote:        post.ConsentNote,
		ReviewNote:         post.ReviewNote,
		ApprovedAt:         post.ApprovedAt,
		ApprovedBy:         post.ApprovedBy,
		PublishedAt:        post.PublishedAt,
		Now:                now,
	})
	if err != nil {
		return Post{}, err
	}
	return loadChannels(ctx, q, saved)
}

func loadChannels(ctx context.Context, q db.Querier, post db.Post) (Post, error) {
	versions, err := q.ListPostVersions(ctx, post.ID)
	if err != nil {
		return Post{}, err
	}
	publications, err := q.ListPostPublications(ctx, post.ID)
	if err != nil {
		return Post{}, err
	}
	return Post{Post: post, Versions: versions, Publications: publications}, nil
}

// asSlugTaken relies on the unique indexes, so two drafts racing for a slug cannot both win.
func asSlugTaken(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if pgErr.ConstraintName == "post_versions_slug_key" || pgErr.ConstraintName == "published_articles_slug_key" {
		return errSlugTaken
	}
	return err
}

func optionalText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
