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

	admin := r.Admin(auth.BookingAdmin)
	admin.GET("/admin/services", h.listServices)
	admin.POST("/admin/services", h.createService)
	admin.GET("/admin/services/:serviceId", h.getService)
	admin.PATCH("/admin/services/:serviceId", h.updateService)
	admin.DELETE("/admin/services/:serviceId", h.deleteService)
	admin.POST("/admin/services/:serviceId/pause", h.pauseService)
	admin.POST("/admin/services/:serviceId/resume", h.resumeService)

	admin.GET("/admin/availability/rules", h.listRules)
	admin.POST("/admin/availability/rules", h.createRule)
	admin.PUT("/admin/availability/rules", h.replaceWeeklyHours)
	admin.PUT("/admin/availability/rules/:ruleId", h.updateRule)
	admin.DELETE("/admin/availability/rules/:ruleId", h.deleteRule)
	admin.GET("/admin/availability/overrides", h.listOverrides)
	admin.POST("/admin/availability/overrides", h.createOverride)
	admin.PUT("/admin/availability/overrides/:overrideId", h.updateOverride)
	admin.DELETE("/admin/availability/overrides/:overrideId", h.deleteOverride)
	admin.GET("/admin/availability/blocks", h.listBlocks)
	admin.POST("/admin/availability/blocks", h.createBlock)
	admin.PUT("/admin/availability/blocks/:blockId", h.updateBlock)
	admin.DELETE("/admin/availability/blocks/:blockId", h.deleteBlock)
	admin.GET("/admin/availability/preview", h.previewAvailability)

	admin.GET("/admin/dashboard", h.dashboard)
	admin.GET("/admin/appointments", h.listAppointments)
	admin.POST("/admin/appointments", h.createManualAppointment)
	admin.GET("/admin/appointments/:appointmentId", h.getAppointment)
	admin.POST("/admin/appointments/:appointmentId/confirm", h.confirmAppointment)
	admin.POST("/admin/appointments/:appointmentId/decline", h.declineAppointment)
	admin.POST("/admin/appointments/:appointmentId/reschedule", h.rescheduleAppointment)
	admin.POST("/admin/appointments/:appointmentId/cancel", h.cancelAppointment)
	admin.POST("/admin/appointments/:appointmentId/complete", h.completeAppointment)
	admin.POST("/admin/appointments/:appointmentId/no-show", h.markNoShow)
	admin.PUT("/admin/appointments/:appointmentId/note", h.setAppointmentNote)
	admin.POST("/admin/appointments/:appointmentId/communications/:communicationId/resend", h.resendCommunication)
	admin.POST("/admin/appointments/:appointmentId/mark-communicated", h.markCommunicated)
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
