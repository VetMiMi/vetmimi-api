// Package media is the media library: images uploaded for posts, kept as a
// re-encoded original and web sizes in the S3-compatible bucket, with their
// alt text and credit in PostgreSQL.
package media

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
)

var (
	errNotFound   = apperr.New(apperr.NotFound, "No media item has this id.")
	errStale      = apperr.New(apperr.StaleVersion, "The media item changed since it was read; reload it.")
	errInUse      = apperr.New(apperr.InUse, "A post uses this image; take it out of the post first.")
	errNoStorage  = apperr.New(apperr.Unavailable, "Media storage is not configured.")
	errTooLarge   = apperr.New(apperr.PayloadTooLarge, "An image may be at most 20 MB.")
	errEmptyImage = apperr.Invalid("No image was uploaded.", apperr.FieldError{Field: "file", Message: "is required"})
)

// Description is what an author says about an image. Alt is localized JSON
// ({"en": …, "my": …}), nil for none.
type Description struct {
	Alt    json.RawMessage
	Credit string
}

// Upload re-encodes data and stores it in store as a new item described by
// d. The row and the objects are saved together: a failed upload leaves no
// row behind.
func Upload(ctx context.Context, pool *pgxpool.Pool, store *Store, data []byte, d Description,
	by pgtype.UUID, now time.Time) (db.Media, error) {
	switch {
	case store == nil:
		return db.Media{}, errNoStorage
	case len(data) == 0:
		return db.Media{}, errEmptyImage
	case len(data) > MaxUploadBytes:
		return db.Media{}, errTooLarge
	}
	img, err := encode(ctx, data)
	if err != nil {
		return db.Media{}, err
	}
	var out db.Media
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		out, err = db.New(tx).CreateMedia(ctx, db.CreateMediaParams{
			Width: int32(img.width), Height: int32(img.height), Widths: img.widths, ByteSize: int64(len(data)),
			Alt: d.Alt, Credit: optionalText(d.Credit), UploadedBy: by, Now: now,
		})
		if err != nil {
			return err
		}
		return putAll(ctx, store, out.ID, img)
	})
	return out, err
}

// putAll stores the original and every web size, removing what it stored
// if one fails, since the row they belong to is rolled back.
func putAll(ctx context.Context, store *Store, id pgtype.UUID, img encoded) error {
	err := store.put(ctx, originalKey(id), img.original)
	for _, w := range img.widths {
		if err != nil {
			break
		}
		err = store.put(ctx, "public/"+sizePath(id, w), img.sizes[w])
	}
	if err != nil {
		_ = store.remove(context.WithoutCancel(ctx), keys(id, img.widths))
	}
	return err
}

// Get reads one item.
func Get(ctx context.Context, q db.Querier, id pgtype.UUID) (db.Media, error) {
	m, err := q.GetMedia(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Media{}, errNotFound
	}
	return m, err
}

// Filter narrows the list; empty fields do not filter.
type Filter struct {
	Search, Cursor string
	Limit          int
}

// Page is one page of items, newest first.
type Page struct {
	Items      []db.Media
	NextCursor string
}

// List lists items newest first, searching alt text in either language and
// the credit.
func List(ctx context.Context, q db.Querier, f Filter) (Page, error) {
	limit := cmp.Or(f.Limit, 50)
	p := db.ListMediaParams{Search: listing.LikePattern(f.Search), MaxRows: int32(limit) + 1}
	if f.Cursor != "" {
		var err error
		if p.AfterAt, p.AfterID, err = listing.DecodeCursor(f.Cursor); err != nil {
			return Page{}, err
		}
	}
	rows, err := q.ListMedia(ctx, p)
	if err != nil {
		return Page{}, err
	}
	page := Page{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = listing.EncodeCursor(last.CreatedAt, last.ID)
	}
	return page, nil
}

// Describe replaces an item's alt text and credit as of version.
func Describe(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, d Description,
	now time.Time) (db.Media, error) {
	m, err := q.UpdateMedia(ctx, db.UpdateMediaParams{
		ID: id, Version: version, Alt: d.Alt, Credit: optionalText(d.Credit), Now: now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := Get(ctx, q, id); err != nil {
			return db.Media{}, err
		}
		return db.Media{}, errStale
	}
	return m, err
}

// Delete deletes an item no post uses, whatever the post's status, and its
// objects. The row stays if the objects cannot be removed, so nothing is
// left served without a record.
func Delete(ctx context.Context, pool *pgxpool.Pool, store *Store, id pgtype.UUID) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		m, err := q.DeleteUnusedMedia(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			if _, err := Get(ctx, q, id); err != nil {
				return err
			}
			return errInUse
		}
		if err != nil || store == nil {
			return err
		}
		return store.remove(ctx, keys(m.ID, m.Widths))
	})
}

func optionalText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
