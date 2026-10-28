package linkedin

import (
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Connector *Connector
}

func (h *Handler) Routes(r web.Routes) {
	admin := r.Admin.Group("", web.Role(auth.SiteAdmin))
	admin.GET("/admin/connections/linkedin", h.getConnection)
	admin.DELETE("/admin/connections/linkedin", h.disconnect)
	admin.POST("/admin/connections/linkedin/authorize", h.startConnection)
	admin.POST("/admin/connections/linkedin/callback", h.finishConnection)
}
