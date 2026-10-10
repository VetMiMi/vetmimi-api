package content

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Article is a post's website version as last published; a list leaves Body and SEO empty.
type Article struct {
	Slug           string
	Kind           string
	Title          string
	Excerpt        string
	Body           string
	SEOTitle       string
	SEODescription string
	Cover          *Cover
	PublishedAt    time.Time
}

type Cover struct {
	ID            pgtype.UUID
	Width, Height int32
	Widths        []int32
	Alt           string
}

var errNoArticle = apperr.New(apperr.NotFound, "No published article has this slug.")

func ListArticles(ctx context.Context, q db.Querier, locale, kind string, limit int) ([]Article, error) {
	rows, err := q.ListPublicArticles(ctx, db.ListPublicArticlesParams{
		Kind:    optionalText(kind),
		MaxRows: int32(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]Article, len(rows))
	for i, r := range rows {
		out[i] = Article{
			Slug: r.Slug, Kind: r.Kind, PublishedAt: r.PublishedAt.Time,
			Title: inLocale(r.Title, locale), Excerpt: inLocale(r.Excerpt, locale),
			Cover: cover(r.CoverID, r.CoverWidth, r.CoverHeight, r.CoverWidths, r.CoverAlt, locale),
		}
	}
	return out, nil
}

func GetArticle(ctx context.Context, q db.Querier, slug, locale string) (Article, error) {
	r, err := q.GetPublicArticle(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, errNoArticle
	}
	if err != nil {
		return Article{}, err
	}
	a := Article{
		Slug: r.Slug, Kind: r.Kind, PublishedAt: r.PublishedAt.Time,
		Cover: cover(r.CoverID, r.CoverWidth, r.CoverHeight, r.CoverWidths, r.CoverAlt, locale),
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

func cover(id pgtype.UUID, width, height pgtype.Int4, widths []int32, alt json.RawMessage, locale string) *Cover {
	if !id.Valid {
		return nil
	}
	return &Cover{ID: id, Width: width.Int32, Height: height.Int32, Widths: widths, Alt: inLocale(alt, locale)}
}

// inLocale falls back to English when raw has no text for locale.
func inLocale(raw json.RawMessage, locale string) string {
	var text map[string]string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	if s := text[locale]; !blank(s) {
		return s
	}
	return text["en"]
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }
