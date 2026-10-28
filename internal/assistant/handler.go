package assistant

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool   *pgxpool.Pool
	Client *Client
}

var suggestionLimit = web.Rule{Name: "ai-suggestions", Limit: 20, Window: time.Hour, By: web.ByUser}

func (h *Handler) Routes(r web.Routes) {
	r.Admin.GET("/admin/ai/status", web.Role(auth.ContentEditor), h.status)
	r.Slow.POST("/admin/posts/:postId/suggestions", web.Role(auth.ContentEditor), r.Limit(suggestionLimit), h.suggest)
}
