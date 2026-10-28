package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
