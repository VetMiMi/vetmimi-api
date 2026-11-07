package content

import (
	"cmp"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type articlePath struct {
	Slug string `uri:"slug" binding:"required,slug,max=120"`
}

type listArticlesQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
	Kind   string `form:"kind" binding:"omitempty,oneof=insight true_story announcement"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
}

type articleQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
}

type articleListResponse struct {
	Locale string                   `json:"locale"`
	Items  []articleSummaryResponse `json:"items"`
}

type articleSummaryResponse struct {
	Slug        string         `json:"slug"`
	Kind        string         `json:"kind"`
	Title       string         `json:"title"`
	Excerpt     string         `json:"excerpt"`
	CoverImage  *imageResponse `json:"coverImage,omitempty"`
	PublishedAt time.Time      `json:"publishedAt"`
}

type articleResponse struct {
	Slug           string         `json:"slug"`
	Kind           string         `json:"kind"`
	Title          string         `json:"title"`
	Excerpt        string         `json:"excerpt"`
	Body           string         `json:"body"`
	CoverImage     *imageResponse `json:"coverImage,omitempty"`
	SeoTitle       string         `json:"seoTitle"`
	SeoDescription string         `json:"seoDescription"`
	PublishedAt    time.Time      `json:"publishedAt"`
}

type imageResponse struct {
	ID     pgtype.UUID  `json:"id"`
	Alt    string       `json:"alt"`
	Width  int32        `json:"width"`
	Height int32        `json:"height"`
	Sizes  []media.Size `json:"sizes"`
}

func (h *Handler) listArticles(c *gin.Context) {
	var query listArticlesQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	limit := 100
	if query.Limit != nil {
		limit = *query.Limit
	}
	locale := cmp.Or(query.Locale, "en")
	articles, err := ListArticles(ctx, db.New(h.Pool), locale, query.Kind, limit)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := articleListResponse{
		Locale: locale,
		Items:  make([]articleSummaryResponse, len(articles)),
	}
	for i, article := range articles {
		response.Items[i] = articleSummaryResponse{
			Slug:        article.Slug,
			Kind:        article.Kind,
			Title:       article.Title,
			Excerpt:     article.Excerpt,
			CoverImage:  h.newImageResponse(article.Cover),
			PublishedAt: article.PublishedAt.UTC(),
		}
	}
	c.JSON(http.StatusOK, response)
}

// getArticle answers the same 404 for an unpublished, archived and unknown slug.
func (h *Handler) getArticle(c *gin.Context) {
	var path articlePath
	if !web.BindURI(c, &path) {
		return
	}
	var query articleQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	locale := cmp.Or(query.Locale, "en")
	article, err := GetArticle(ctx, db.New(h.Pool), path.Slug, locale)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := articleResponse{
		Slug:           article.Slug,
		Kind:           article.Kind,
		Title:          article.Title,
		Excerpt:        article.Excerpt,
		Body:           article.Body,
		CoverImage:     h.newImageResponse(article.Cover),
		SeoTitle:       article.SEOTitle,
		SeoDescription: article.SEODescription,
		PublishedAt:    article.PublishedAt.UTC(),
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) newImageResponse(cover *Cover) *imageResponse {
	if cover == nil {
		return nil
	}
	return &imageResponse{
		ID:     cover.ID,
		Alt:    cover.Alt,
		Width:  cover.Width,
		Height: cover.Height,
		Sizes:  media.Sizes(h.MediaPublicURL, cover.ID, cover.Widths),
	}
}
