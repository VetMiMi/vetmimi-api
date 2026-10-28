package media

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool      *pgxpool.Pool
	Store     *Store // nil when no bucket is configured: uploads are refused
	PublicURL string // MEDIA_PUBLIC_URL, where the web sizes are served
	Now       clock.Now
}

func (h *Handler) Routes(r web.Routes) {
	editor := web.Role(auth.ContentEditor)
	r.Admin.GET("/admin/media", editor, h.listMedia)
	r.Slow.POST("/admin/media", editor, h.uploadMedia)
	r.Admin.GET("/admin/media/:mediaId", editor, h.getMedia)
	r.Admin.PATCH("/admin/media/:mediaId", editor, h.updateMedia)
	r.Admin.DELETE("/admin/media/:mediaId", editor, h.deleteMedia)
}
