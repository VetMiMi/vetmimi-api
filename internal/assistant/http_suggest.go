package assistant

import (
	"cmp"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type suggestBody struct {
	Channels []string `json:"channels" binding:"required,min=1,unique,dive,oneof=facebook instagram linkedin website"`
	Language string   `json:"language" binding:"omitempty,oneof=en my"`
}

type statusResponse struct {
	Enabled bool `json:"enabled"`
}

type suggestionsResponse struct {
	Facebook  *Text                      `json:"facebook,omitempty"`
	Instagram *Instagram                 `json:"instagram,omitempty"`
	LinkedIn  *Text                      `json:"linkedin,omitempty"`
	Website   *websiteSuggestionResponse `json:"website,omitempty"`
}

type websiteSuggestionResponse struct {
	Language string `json:"language"`
	Website
}

// status lets the portal hide the assistant while it is off.
func (h *Handler) status(c *gin.Context) {
	c.JSON(http.StatusOK, statusResponse{Enabled: h.Client.Enabled()})
}

// suggest saves nothing: the editor edits a suggestion and saves the post.
func (h *Handler) suggest(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body suggestBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	post, err := content.GetPost(ctx, db.New(h.Pool), postID)
	if err != nil {
		web.Error(c, err)
		return
	}
	request := Request{
		Channels: body.Channels,
		Language: cmp.Or(body.Language, "en"),
	}
	suggestions, err := h.Client.Suggest(ctx, post, request)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "post_suggested", "request_id", web.RequestID(ctx), "post_id", postID.String(),
		"channels", request.Channels)
	c.JSON(http.StatusOK, newSuggestionsResponse(suggestions, request.Language))
}

func newSuggestionsResponse(suggestions Suggestions, language string) suggestionsResponse {
	response := suggestionsResponse{
		Facebook:  suggestions.Facebook,
		Instagram: suggestions.Instagram,
		LinkedIn:  suggestions.LinkedIn,
	}
	if suggestions.Website != nil {
		response.Website = &websiteSuggestionResponse{Language: language, Website: *suggestions.Website}
	}
	return response
}
