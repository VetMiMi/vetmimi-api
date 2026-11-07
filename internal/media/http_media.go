package media

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listMediaQuery struct {
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  int    `form:"limit,default=50" binding:"min=1,max=100"`
}

type updateMediaBody struct {
	Version int          `json:"version" binding:"required,min=1"`
	Alt     *altTextBody `json:"alt"`
	Credit  string       `json:"credit" binding:"max=300"`
}

// altTextBody is stored as sent, so its omitempty tags shape the saved JSON.
type altTextBody struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=300"`
	My *string `json:"my,omitempty" binding:"omitnil,max=300"`
}

type mediaListResponse struct {
	Items      []mediaResponse `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

type mediaResponse struct {
	ID         string           `json:"id"`
	Width      int32            `json:"width"`
	Height     int32            `json:"height"`
	ByteSize   int64            `json:"byteSize"`
	Alt        *altTextResponse `json:"alt,omitempty"`
	Credit     string           `json:"credit,omitempty"`
	Sizes      []Size           `json:"sizes"`
	UploadedBy string           `json:"uploadedBy,omitempty"`
	Version    int32            `json:"version"`
	CreatedAt  time.Time        `json:"createdAt"`
	UpdatedAt  time.Time        `json:"updatedAt"`
}

type altTextResponse struct {
	En *string `json:"en,omitempty"`
	My *string `json:"my,omitempty"`
}

func (h *Handler) listMedia(c *gin.Context) {
	var query listMediaQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	filter := Filter(query) // the query has exactly Filter's fields, plus binding tags
	page, err := List(ctx, db.New(h.Pool), filter)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := mediaListResponse{
		Items:      make([]mediaResponse, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for i, item := range page.Items {
		response.Items[i] = h.newMediaResponse(item)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) getMedia(c *gin.Context) {
	mediaID, ok := web.PathUUID(c, "mediaId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	item, err := Get(ctx, db.New(h.Pool), mediaID)
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, h.newMediaResponse(item))
}

// updateMedia replaces the alt text and credit; one left out is cleared.
func (h *Handler) updateMedia(c *gin.Context) {
	mediaID, ok := web.PathUUID(c, "mediaId")
	if !ok {
		return
	}
	var body updateMediaBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	description := Description{Credit: body.Credit}
	if body.Alt != nil {
		description.Alt, _ = json.Marshal(body.Alt) // two optional strings always marshal
	}
	item, err := Describe(ctx, db.New(h.Pool), mediaID, int32(body.Version), description, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "media_described", "request_id", web.RequestID(ctx), "media_id", item.ID.String())
	c.JSON(http.StatusOK, h.newMediaResponse(item))
}

func (h *Handler) deleteMedia(c *gin.Context) {
	mediaID, ok := web.PathUUID(c, "mediaId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := Delete(ctx, h.Pool, h.Store, mediaID)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "media_deleted", "request_id", web.RequestID(ctx), "media_id", mediaID.String())
	c.Status(http.StatusNoContent)
}

func (h *Handler) newMediaResponse(item db.Media) mediaResponse {
	response := mediaResponse{
		ID:        item.ID.String(),
		Width:     item.Width,
		Height:    item.Height,
		ByteSize:  item.ByteSize,
		Credit:    item.Credit.String,
		Sizes:     Sizes(h.PublicURL, item.ID, item.Widths),
		Version:   item.Version,
		CreatedAt: item.CreatedAt.UTC(),
		UpdatedAt: item.UpdatedAt.UTC(),
	}
	if item.UploadedBy.Valid {
		response.UploadedBy = item.UploadedBy.String()
	}
	if item.Alt != nil {
		var alt altTextResponse
		if json.Unmarshal(item.Alt, &alt) == nil {
			response.Alt = &alt
		}
	}
	return response
}
