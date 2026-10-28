package video

import (
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Pool    *pgxpool.Pool
	Hub     *Hub
	Issuer  Issuer // its Secret also checks the tickets a socket brings
	SiteURL string // the only origin a room socket opens from
	Now     clock.Now
}

var (
	sessionLimit = web.Rule{Name: "video-session", Limit: 30, Window: time.Minute, By: web.ByToken}
	roomLimit    = web.Rule{Name: "room", Limit: 20, Window: time.Minute, By: byRoom}
)

func (h *Handler) Routes(r web.Routes) {
	r.Public.GET("/public/sessions/:token", r.Limit(sessionLimit), h.getSession)
	r.Public.POST("/public/sessions/:token/ticket", r.Limit(sessionLimit), h.createClientTicket)

	admin := r.Admin(auth.BookingAdmin)
	admin.POST("/admin/appointments/:appointmentId/video-session", h.startSession)
	admin.POST("/admin/appointments/:appointmentId/video-session/end", h.endSession)

	// The socket stays open for a whole session, so it skips the groups' deadline and body cap.
	r.Root.GET("/video/rooms/:roomId/ws", h.checkRoomRequest, r.Limit(roomLimit), h.connectRoom)
}

// byRoom counts the parsed id, so the same room written differently is still one room.
func byRoom(c *gin.Context) string {
	id, _ := roomIDOf(c)
	return id.String()
}

func roomIDOf(c *gin.Context) (pgtype.UUID, error) {
	var id pgtype.UUID
	err := id.Scan(c.Param("roomId"))
	return id, err
}
