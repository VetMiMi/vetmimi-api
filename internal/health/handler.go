// Package health answers the host's probes: /healthz while the process runs,
// /readyz while PostgreSQL and Redis answer too.
package health

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	PingPostgres func(context.Context) error
	PingRedis    func(context.Context) error
}

// Routes are on Root: a probe carries no service key and needs no body cap.
func (h *Handler) Routes(r web.Routes) {
	r.Root.GET("/healthz", h.healthz)
	r.Root.GET("/readyz", h.readyz)
}
