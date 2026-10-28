package settings

import (
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool  *pgxpool.Pool
	Queue *queue.Queue // nil in tests: tasks are dropped
	Now   clock.Now
}

func (h *Handler) Routes(r web.Routes) {
	r.Admin(auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin).GET("/admin/settings", h.getSettings)
	r.Admin(auth.BookingAdmin).PATCH("/admin/settings", h.updateSettings)
}
