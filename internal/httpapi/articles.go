package httpapi

import (
	"cmp"
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func (s *server) ListPublicArticles(ctx context.Context, req gen.ListPublicArticlesRequestObject) (gen.ListPublicArticlesResponseObject, error) {
	p := req.Params
	locale := cmp.Or(deref(p.Locale), gen.LocaleEn)
	articles, err := content.ListArticles(ctx, db.New(s.Pool), string(locale), string(deref(p.Kind)),
		cmp.Or(deref(p.Limit), 100))
	if err != nil {
		return nil, err
	}
	out := gen.ListPublicArticles200JSONResponse{Locale: locale, Items: make([]gen.PublicArticleSummary, len(articles))}
	for i, a := range articles {
		out.Items[i] = gen.PublicArticleSummary{Slug: a.Slug, Kind: gen.PostKind(a.Kind), Title: a.Title,
			Excerpt: a.Excerpt, CoverImage: s.coverView(a.Cover), PublishedAt: a.PublishedAt.UTC()}
	}
	return out, nil
}

func (s *server) GetPublicArticle(ctx context.Context, req gen.GetPublicArticleRequestObject) (gen.GetPublicArticleResponseObject, error) {
	locale := cmp.Or(deref(req.Params.Locale), gen.LocaleEn)
	a, err := content.GetArticle(ctx, db.New(s.Pool), req.Slug, string(locale))
	if err != nil {
		return nil, err
	}
	return gen.GetPublicArticle200JSONResponse{Slug: a.Slug, Kind: gen.PostKind(a.Kind), Title: a.Title,
		Excerpt: a.Excerpt, Body: a.Body, SeoTitle: a.SEOTitle, SeoDescription: a.SEODescription,
		CoverImage: s.coverView(a.Cover), PublishedAt: a.PublishedAt.UTC()}, nil
}

func (s *server) coverView(c *content.Cover) *gen.PublicImage {
	if c == nil {
		return nil
	}
	return &gen.PublicImage{Id: openapi_types.UUID(c.ID.Bytes), Alt: c.Alt, Width: int(c.Width),
		Height: int(c.Height), Sizes: s.imageSizes(c.ID, c.Widths)}
}
