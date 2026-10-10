// Package media is the image library for posts.
// Upload re-encodes an image to JPEGs, stores them in the S3 bucket and
// records its size, alt text and credit in PostgreSQL.
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

// Description is an image's alt text, as localized JSON ({"en": …, "my": …}), and credit.
type Description struct {
	Alt    json.RawMessage
	Credit string
}

// Upload stores a new image. The row and the objects are saved together or not at all.
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
		if err := putAll(ctx, store, out.ID, img); err != nil {
			// The row is rolled back, so remove the objects already stored.
			_ = store.remove(context.WithoutCancel(ctx), keys(out.ID, img.widths))
			return err
		}
		return nil
	})
	return out, err
}

func putAll(ctx context.Context, store *Store, id pgtype.UUID, img encoded) error {
	if err := store.put(ctx, originalKey(id), img.original); err != nil {
		return err
	}
	for _, w := range img.widths {
		if err := store.put(ctx, publicKey(id, w), img.sizes[w]); err != nil {
			return err
		}
	}
	return nil
}

func Get(ctx context.Context, q db.Querier, id pgtype.UUID) (db.Media, error) {
	m, err := q.GetMedia(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Media{}, errNotFound
	}
	return m, err
}

// Filter narrows List; empty fields do not filter.
type Filter struct {
	Search, Cursor string
	Limit          int
}

type Page struct {
	Items      []db.Media
	NextCursor string
}

// List returns items newest first, searching alt text in either language and the credit.
func List(ctx context.Context, q db.Querier, f Filter) (Page, error) {
	limit := cmp.Or(f.Limit, 50)
	params := db.ListMediaParams{Search: listing.LikePattern(f.Search), MaxRows: int32(limit) + 1}
	if f.Cursor != "" {
		var err error
		params.AfterAt, params.AfterID, err = listing.DecodeCursor(f.Cursor)
		if err != nil {
			return Page{}, err
		}
	}
	rows, err := q.ListMedia(ctx, params)
	if err != nil {
		return Page{}, err
	}
	if len(rows) <= limit {
		return Page{Items: rows}, nil
	}
	items := rows[:limit]
	last := items[limit-1]
	return Page{Items: items, NextCursor: listing.EncodeCursor(last.CreatedAt, last.ID)}, nil
}

// Describe replaces an item's alt text and credit, if it is still at version.
func Describe(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, d Description,
	now time.Time) (db.Media, error) {
	m, err := q.UpdateMedia(ctx, db.UpdateMediaParams{
		ID: id, Version: version, Alt: d.Alt, Credit: optionalText(d.Credit), Now: now,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	if _, err := Get(ctx, q, id); err != nil {
		return db.Media{}, err
	}
	return db.Media{}, errStale
}

// Delete deletes an item no post uses, and its objects. The row stays if
// the objects cannot be removed.
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
		if err != nil {
			return err
		}
		if store == nil {
			return nil
		}
		return store.remove(ctx, keys(m.ID, m.Widths))
	})
}

func optionalText(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
