package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/video"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool   *pgxpool.Pool
	Secret []byte       // signs management links
	Queue  *queue.Queue // nil in tests: tasks are dropped and the sweepers rebuild them
	Hub    *video.Hub
	Now    clock.Now
}

var (
	availabilityLimit = web.Rule{Name: "availability", Limit: 60, Window: time.Minute, By: web.ByVisitorIP}
	requestLimit      = web.Rule{Name: "booking", Limit: 5, Window: 10 * time.Minute, By: web.ByVisitorIP}
	manageLimit       = web.Rule{Name: "manage", Limit: 20, Window: time.Hour, By: web.ByToken}
)

func (h *Handler) Routes(r web.Routes) {
	r.Public.GET("/public/booking/services", h.listPublicServices)
	r.Public.GET("/public/availability", r.Limit(availabilityLimit), h.publicAvailability)
	r.Public.POST("/public/appointments", r.Limit(requestLimit), h.requestAppointment)
	r.Public.GET("/public/manage/:token", r.Limit(manageLimit), h.getManaged)
	r.Public.POST("/public/manage/:token/cancel", r.Limit(manageLimit), h.cancelManaged)
	r.Public.POST("/public/manage/:token/reschedule-request", r.Limit(manageLimit), h.requestManagedReschedule)

	setup := r.Admin.Group("", web.Role(auth.BookingAdmin))
	setup.GET("/admin/services", h.listServices)
	setup.POST("/admin/services", h.createService)
	setup.GET("/admin/services/:serviceId", h.getService)
	setup.PATCH("/admin/services/:serviceId", h.updateService)
	setup.DELETE("/admin/services/:serviceId", h.deleteService)
	setup.POST("/admin/services/:serviceId/pause", h.pauseService)
	setup.POST("/admin/services/:serviceId/resume", h.resumeService)

	setup.GET("/admin/availability/rules", h.listRules)
	setup.POST("/admin/availability/rules", h.createRule)
	setup.PUT("/admin/availability/rules", h.replaceWeeklyHours)
	setup.PUT("/admin/availability/rules/:ruleId", h.updateRule)
	setup.DELETE("/admin/availability/rules/:ruleId", h.deleteRule)
	setup.GET("/admin/availability/overrides", h.listOverrides)
	setup.POST("/admin/availability/overrides", h.createOverride)
	setup.PUT("/admin/availability/overrides/:overrideId", h.updateOverride)
	setup.DELETE("/admin/availability/overrides/:overrideId", h.deleteOverride)
	setup.GET("/admin/availability/blocks", h.listBlocks)
	setup.POST("/admin/availability/blocks", h.createBlock)
	setup.PUT("/admin/availability/blocks/:blockId", h.updateBlock)
	setup.DELETE("/admin/availability/blocks/:blockId", h.deleteBlock)
	setup.GET("/admin/availability/preview", h.previewAvailability)
}

// afterCommit hands the queue what a committed change asks of it and closes a video room it ended.
func (h *Handler) afterCommit(ctx context.Context, changed Changed) {
	if h.Queue != nil {
		h.Queue.Remove(changed.Remove...)
		h.Queue.Replace(ctx, changed.Replace...)
		h.Queue.Enqueue(ctx, changed.Tasks...)
	}
	if h.Hub != nil {
		h.Hub.EndRoom(changed.EndedRoom)
	}
}
