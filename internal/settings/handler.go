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
	r.Admin.GET("/admin/settings",
		web.Role(auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin), h.getSettings)
	r.Admin.PATCH("/admin/settings", web.Role(auth.BookingAdmin), h.updateSettings)
}
