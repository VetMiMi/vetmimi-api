// Package media is the image library for posts.
// Upload re-encodes an image to JPEGs, stores them in the S3 bucket and
// records its size, alt text and credit in PostgreSQL.
package media

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
)

// Description is an image's alt text, as localized JSON ({"en": …, "my": …}), and credit.
type Description struct {
	Alt    json.RawMessage
	Credit string
}

// Filter narrows List; empty fields do not filter.
type Filter struct {
	Search string
	Cursor string
	Limit  int
}

type Page struct {
	Items      []db.Media
	NextCursor string
}

var (
	errNotFound   = apperr.New(apperr.NotFound, "No media item has this id.")
	errStale      = apperr.New(apperr.StaleVersion, "The media item changed since it was read; reload it.")
	errInUse      = apperr.New(apperr.InUse, "A post uses this image; take it out of the post first.")
	errNoStorage  = apperr.New(apperr.Unavailable, "Media storage is not configured.")
	errTooLarge   = apperr.New(apperr.PayloadTooLarge, "An image may be at most 20 MB.")
	errEmptyImage = apperr.Invalid("No image was uploaded.", apperr.FieldError{Field: "file", Message: "is required"})
)

// Upload stores a new image. The row and the objects are saved together or not at all.
func Upload(ctx context.Context, pool *pgxpool.Pool, store *Store, data []byte, description Description,
	uploadedBy pgtype.UUID, now time.Time) (db.Media, error) {
	switch {
	case store == nil:
		return db.Media{}, errNoStorage
	case len(data) == 0:
		return db.Media{}, errEmptyImage
	case len(data) > MaxUploadBytes:
		return db.Media{}, errTooLarge
	}
	image, err := encode(ctx, data)
	if err != nil {
		return db.Media{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.Media{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	item, err := q.CreateMedia(ctx, db.CreateMediaParams{
		Width:      int32(image.width),
		Height:     int32(image.height),
		Widths:     image.widths,
		ByteSize:   int64(len(data)),
		Alt:        description.Alt,
		Credit:     optionalText(description.Credit),
		UploadedBy: uploadedBy,
		Now:        now,
	})
	if err != nil {
		return db.Media{}, err
	}
	err = putAll(ctx, store, item.ID, image)
	if err != nil {
		// The row is rolled back, so remove the objects already stored.
		_ = store.remove(context.WithoutCancel(ctx), keys(item.ID, image.widths))
		return db.Media{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		removeIfNotSaved(ctx, pool, store, item.ID, image.widths)
		return db.Media{}, err
	}
	return item, nil
}

func Get(ctx context.Context, q db.Querier, id pgtype.UUID) (db.Media, error) {
	item, err := q.GetMedia(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Media{}, errNotFound
	}
	return item, err
}

// List returns items newest first, searching alt text in either language and the credit.
func List(ctx context.Context, q db.Querier, filter Filter) (Page, error) {
	limit := cmp.Or(filter.Limit, 50)
	params := db.ListMediaParams{
		Search:  listing.LikePattern(filter.Search),
		MaxRows: int32(limit) + 1,
	}
	if filter.Cursor != "" {
		afterAt, afterID, err := listing.DecodeCursor(filter.Cursor)
		if err != nil {
			return Page{}, err
		}
		params.AfterAt = afterAt
		params.AfterID = afterID
	}

	rows, err := q.ListMedia(ctx, params)
	if err != nil {
		return Page{}, err
	}
	if len(rows) <= limit {
		return Page{Items: rows}, nil
	}
	last := rows[limit-1]
	page := Page{
		Items:      rows[:limit],
		NextCursor: listing.EncodeCursor(last.CreatedAt, last.ID),
	}
	return page, nil
}

// Describe replaces an item's alt text and credit, if it is still at version.
func Describe(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, description Description,
	now time.Time) (db.Media, error) {
	item, err := q.UpdateMedia(ctx, db.UpdateMediaParams{
		ID:      id,
		Version: version,
		Alt:     description.Alt,
		Credit:  optionalText(description.Credit),
		Now:     now,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		return item, err
	}
	_, err = Get(ctx, q, id)
	if err != nil {
		return db.Media{}, err
	}
	return db.Media{}, errStale
}

// Delete deletes an item no post uses, then its objects. If the objects cannot
// be removed, the item is still deleted and unused objects are left behind.
func Delete(ctx context.Context, pool *pgxpool.Pool, store *Store, id pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	item, err := q.DeleteUnusedMedia(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		_, err = Get(ctx, q, id)
		if err != nil {
			return err
		}
		return errInUse
	}
	if err != nil {
		return err
	}
	err = tx.Commit(ctx)
	if err != nil {
		return err
	}

	// Removed only after the commit: a failure leaves unused files behind, never
	// an item whose files are gone.
	if store != nil {
		err = store.remove(ctx, keys(item.ID, item.Widths))
		if err != nil {
			slog.WarnContext(ctx, "media_files_left", "media_id", item.ID.String(), "err", err)
		}
	}
	return nil
}

func putAll(ctx context.Context, store *Store, id pgtype.UUID, image encoded) error {
	err := store.put(ctx, originalKey(id), image.original)
	if err != nil {
		return err
	}
	for _, width := range image.widths {
		err = store.put(ctx, publicKey(id, width), image.sizes[width])
		if err != nil {
			return err
		}
	}
	return nil
}

func optionalText(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

// removeIfNotSaved removes an upload's files after a failed commit. A commit can
// fail after the row was saved; then the files are in use and stay.
func removeIfNotSaved(ctx context.Context, pool *pgxpool.Pool, store *Store, id pgtype.UUID, widths []int32) {
	ctx = context.WithoutCancel(ctx)
	_, err := Get(ctx, db.New(pool), id)
	if !errors.Is(err, errNotFound) {
		return
	}
	_ = store.remove(ctx, keys(id, widths))
}
