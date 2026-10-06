package content

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// Article is a published website version in one locale. Body, SEOTitle and
// SEODescription are empty in a list.
type Article struct {
	Slug           string
	Kind           string
	Title          string
	Excerpt        string
	Body           string
	SEOTitle       string
	SEODescription string
	CoverImageID   pgtype.UUID
	PublishedAt    time.Time
}

var errNoArticle = apperr.New(apperr.NotFound, "No published article has this slug.")

// ListArticles lists the published articles newest first, of kind when it
// is not empty, in locale with English as the fallback.
func ListArticles(ctx context.Context, q db.Querier, locale, kind string, limit int) ([]Article, error) {
	rows, err := q.ListPublicArticles(ctx, db.ListPublicArticlesParams{
		Kind:    pgtype.Text{String: kind, Valid: kind != ""},
		MaxRows: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Article, len(rows))
	for i, r := range rows {
		out[i] = Article{
			Slug: r.Slug.String, Kind: r.Kind, CoverImageID: r.CoverImageID, PublishedAt: r.PublishedAt.Time,
			Title: inLocale(r.Title, locale), Excerpt: inLocale(r.Excerpt, locale),
		}
	}
	return out, nil
}

// GetArticle reads one published article in locale, English as the
// fallback. The SEO title and description default to the title and excerpt.
func GetArticle(ctx context.Context, q db.Querier, slug, locale string) (Article, error) {
	r, err := q.GetPublicArticle(ctx, pgtype.Text{String: slug, Valid: true})
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, errNoArticle
	}
	if err != nil {
		return Article{}, err
	}
	a := Article{
		Slug: r.Slug.String, Kind: r.Kind, CoverImageID: r.CoverImageID, PublishedAt: r.PublishedAt.Time,
		Title: inLocale(r.Title, locale), Excerpt: inLocale(r.Excerpt, locale), Body: inLocale(r.Body, locale),
		SEOTitle: inLocale(r.SeoTitle, locale), SEODescription: inLocale(r.SeoDescription, locale),
	}
	if blank(a.SEOTitle) {
		a.SEOTitle = a.Title
	}
	if blank(a.SEODescription) {
		a.SEODescription = a.Excerpt
	}
	return a, nil
}
