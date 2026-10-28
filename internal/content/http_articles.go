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

type articleListQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
	Kind   string `form:"kind" binding:"omitempty,oneof=insight true_story announcement"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
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

type imageResponse struct {
	ID     pgtype.UUID  `json:"id"`
	Alt    string       `json:"alt"`
	Width  int32        `json:"width"`
	Height int32        `json:"height"`
	Sizes  []media.Size `json:"sizes"`
}

func (h *Handler) listArticles(c *gin.Context) {
	var q articleListQuery
	if !web.BindQuery(c, &q) {
		return
	}
	limit := 100
	if q.Limit != nil {
		limit = *q.Limit
	}
	locale := cmp.Or(q.Locale, "en")
	articles, err := ListArticles(c.Request.Context(), db.New(h.Pool), locale, q.Kind, limit)
	if err != nil {
		web.Error(c, err)
		return
	}
	out := articleListResponse{Locale: locale, Items: make([]articleSummaryResponse, len(articles))}
	for i, a := range articles {
		out.Items[i] = articleSummaryResponse{Slug: a.Slug, Kind: a.Kind, Title: a.Title, Excerpt: a.Excerpt,
			CoverImage: h.coverView(a.Cover), PublishedAt: a.PublishedAt.UTC()}
	}
	c.JSON(http.StatusOK, out)
}

type articleURI struct {
	Slug string `uri:"slug" binding:"required,slug,max=120"`
}

type articleQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
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

// getArticle answers the same 404 for an unpublished, archived and unknown slug.
func (h *Handler) getArticle(c *gin.Context) {
	var u articleURI
	var q articleQuery
	if !web.BindURI(c, &u) || !web.BindQuery(c, &q) {
		return
	}
	a, err := GetArticle(c.Request.Context(), db.New(h.Pool), u.Slug, cmp.Or(q.Locale, "en"))
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, articleResponse{Slug: a.Slug, Kind: a.Kind, Title: a.Title, Excerpt: a.Excerpt,
		Body: a.Body, CoverImage: h.coverView(a.Cover), SeoTitle: a.SEOTitle, SeoDescription: a.SEODescription,
		PublishedAt: a.PublishedAt.UTC()})
}

func (h *Handler) coverView(c *Cover) *imageResponse {
	if c == nil {
		return nil
	}
	return &imageResponse{ID: c.ID, Alt: c.Alt, Width: c.Width, Height: c.Height,
		Sizes: media.Sizes(h.MediaPublicURL, c.ID, c.Widths)}
}
