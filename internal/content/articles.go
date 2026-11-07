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
	ID     pgtype.UUID
	Width  int32
	Height int32
	Widths []int32
	Alt    string
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

	articles := make([]Article, len(rows))
	for i, row := range rows {
		articles[i] = Article{
			Slug:        row.Slug,
			Kind:        row.Kind,
			Title:       inLocale(row.Title, locale),
			Excerpt:     inLocale(row.Excerpt, locale),
			Cover:       newCover(row.CoverID, row.CoverWidth, row.CoverHeight, row.CoverWidths, row.CoverAlt, locale),
			PublishedAt: row.PublishedAt.Time,
		}
	}
	return articles, nil
}

func GetArticle(ctx context.Context, q db.Querier, slug, locale string) (Article, error) {
	row, err := q.GetPublicArticle(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Article{}, errNoArticle
	}
	if err != nil {
		return Article{}, err
	}

	article := Article{
		Slug:           row.Slug,
		Kind:           row.Kind,
		Title:          inLocale(row.Title, locale),
		Excerpt:        inLocale(row.Excerpt, locale),
		Body:           inLocale(row.Body, locale),
		SEOTitle:       inLocale(row.SeoTitle, locale),
		SEODescription: inLocale(row.SeoDescription, locale),
		Cover:          newCover(row.CoverID, row.CoverWidth, row.CoverHeight, row.CoverWidths, row.CoverAlt, locale),
		PublishedAt:    row.PublishedAt.Time,
	}
	if blank(article.SEOTitle) {
		article.SEOTitle = article.Title
	}
	if blank(article.SEODescription) {
		article.SEODescription = article.Excerpt
	}
	return article, nil
}

func newCover(id pgtype.UUID, width, height pgtype.Int4, widths []int32, alt json.RawMessage, locale string) *Cover {
	if !id.Valid {
		return nil
	}
	return &Cover{
		ID:     id,
		Width:  width.Int32,
		Height: height.Int32,
		Widths: widths,
		Alt:    inLocale(alt, locale),
	}
}

// inLocale falls back to English when raw has no text for locale.
func inLocale(raw json.RawMessage, locale string) string {
	var text map[string]string
	err := json.Unmarshal(raw, &text)
	if err != nil {
		return ""
	}
	if !blank(text[locale]) {
		return text[locale]
	}
	return text["en"]
}

func blank(s string) bool {
	return strings.TrimSpace(s) == ""
}
