package meta

import (
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Connector *Connector
}

func (h *Handler) Routes(r web.Routes) {
	admin := r.Admin.Group("", web.Role(auth.SiteAdmin))
	admin.GET("/admin/connections/meta", h.getConnection)
	admin.DELETE("/admin/connections/meta", h.disconnect)
	admin.POST("/admin/connections/meta/authorize", h.startConnection)
	admin.POST("/admin/connections/meta/callback", h.finishConnection)
	admin.PUT("/admin/connections/meta/page", h.choosePage)
}
