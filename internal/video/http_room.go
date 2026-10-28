package video

import (
	"context"
	"errors"
	"log/slog"
	"net/url"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// checkRoomRequest answers as a problem what can be refused before the upgrade.
func (h *Handler) checkRoomRequest(c *gin.Context) {
	site, err := url.Parse(h.SiteURL)
	if err != nil || site.Host == "" || c.GetHeader("Origin") != site.Scheme+"://"+site.Host {
		web.Error(c, apperr.New(apperr.Forbidden, "Video rooms open only from the VetMiMi site."))
		return
	}
	ticket := c.Query("ticket")
	if _, err := roomIDOf(c); err != nil || ticket == "" || len(ticket) > 1000 {
		web.Error(c, apperr.New(apperr.InvalidRequest, "A room id and a ticket are required."))
	}
}

// connectRoom opens a participant's signalling WebSocket.
func (h *Handler) connectRoom(c *gin.Context) {
	site, _ := url.Parse(h.SiteURL) // checkRoomRequest has parsed it
	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{OriginPatterns: []string{site.Host}})
	if err != nil {
		return // Accept has answered the request.
	}
	roomID, _ := roomIDOf(c)
	h.joinRoom(c.Request.Context(), conn, roomID, c.Query("ticket"))
}

// Refused with close codes after the upgrade: a browser cannot read why a handshake failed.
func (h *Handler) joinRoom(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, ticket string) {
	now := h.Now()
	role, err := VerifyTicket(h.Issuer.Secret, ticket, roomID, now)
	if err != nil {
		slog.InfoContext(ctx, "video_refused", "request_id", web.RequestID(ctx), "room_id", roomID.String(),
			"code", int(CloseTicketInvalid))
		_ = conn.Close(CloseTicketInvalid, "ticket invalid or expired")
		return
	}
	room, err := OpenRoom(ctx, db.New(h.Pool), roomID, now)
	if errors.Is(err, ErrRoomOver) {
		_ = conn.Close(CloseRoomEnded, "room ended")
		return
	}
	if err != nil {
		slog.ErrorContext(ctx, "video_room_read_failed", "request_id", web.RequestID(ctx), "err", err)
		_ = conn.Close(websocket.StatusInternalError, "unexpected failure")
		return
	}
	h.Hub.Serve(ctx, conn, roomID, role, room.State, web.RequestID(ctx))
}
