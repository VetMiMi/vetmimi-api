package assistant

import (
	"cmp"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type statusResponse struct {
	Enabled bool `json:"enabled"`
}

// status lets the portal hide the assistant while it is off.
func (h *Handler) status(c *gin.Context) {
	c.JSON(http.StatusOK, statusResponse{Enabled: h.Client.Enabled()})
}

type postURI struct {
	PostID string `uri:"postId" binding:"required,uuid"`
}

type suggestInput struct {
	Channels []string `json:"channels" binding:"required,min=1,unique,dive,oneof=facebook instagram linkedin website"`
	Language string   `json:"language" binding:"omitempty,oneof=en my"`
}

type suggestionsResponse struct {
	Facebook  *Text              `json:"facebook,omitempty"`
	Instagram *Instagram         `json:"instagram,omitempty"`
	LinkedIn  *Text              `json:"linkedin,omitempty"`
	Website   *websiteSuggestion `json:"website,omitempty"`
}

type websiteSuggestion struct {
	Language string `json:"language"`
	Website
}

// suggest saves nothing: the editor edits a suggestion and saves the post.
func (h *Handler) suggest(c *gin.Context) {
	var u postURI
	var in suggestInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	id := pgtype.UUID{Bytes: uuid.MustParse(u.PostID), Valid: true} // BindURI has checked it
	p, err := content.GetPost(ctx, db.New(h.Pool), id)
	if err != nil {
		web.Error(c, err)
		return
	}
	r := Request{Channels: in.Channels, Language: cmp.Or(in.Language, "en")}
	got, err := h.Client.Suggest(ctx, p, r)
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "post_suggested", "request_id", web.RequestID(ctx), "post_id", u.PostID,
		"channels", r.Channels)
	out := suggestionsResponse{Facebook: got.Facebook, Instagram: got.Instagram, LinkedIn: got.LinkedIn}
	if got.Website != nil {
		out.Website = &websiteSuggestion{Language: r.Language, Website: *got.Website}
	}
	c.JSON(http.StatusOK, out)
}
