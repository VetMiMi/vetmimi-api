package comms

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool  *pgxpool.Pool
	Queue *queue.Queue // nil in tests: tasks are dropped and the sweepers rebuild them
	Now   clock.Now
}

var enquiryLimit = web.Rule{Name: "contact", Limit: 5, Window: time.Hour, By: web.ByVisitorIP}

func (h *Handler) Routes(r web.Routes) {
	r.Public.POST("/public/contact-enquiries", r.Limit(enquiryLimit), h.createEnquiry)

	admin := r.Admin(auth.BookingAdmin)
	admin.GET("/admin/contact-enquiries", h.listEnquiries)
	admin.GET("/admin/contact-enquiries/:enquiryId", h.getEnquiry)
	admin.POST("/admin/contact-enquiries/:enquiryId/mark-handled", h.markEnquiryHandled)
}
