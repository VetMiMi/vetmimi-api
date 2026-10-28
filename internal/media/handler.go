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
	editor := r.Admin(auth.ContentEditor)
	editor.GET("/admin/media", h.listMedia)
	r.Slow(auth.ContentEditor).POST("/admin/media", h.uploadMedia)
	editor.GET("/admin/media/:mediaId", h.getMedia)
	editor.PATCH("/admin/media/:mediaId", h.updateMedia)
	editor.DELETE("/admin/media/:mediaId", h.deleteMedia)
}
