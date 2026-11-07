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
	_, err = pathRoomID(c)
	ticket := c.Query("ticket")
	if err != nil || ticket == "" || len(ticket) > 1000 {
		web.Error(c, apperr.New(apperr.InvalidRequest, "A room id and a ticket are required."))
	}
}

// connectRoom opens a participant's signalling WebSocket.
func (h *Handler) connectRoom(c *gin.Context) {
	roomID, _ := pathRoomID(c) // checkRoomRequest has checked it
	ticket := c.Query("ticket")
	ctx := c.Request.Context()

	site, _ := url.Parse(h.SiteURL) // checkRoomRequest has parsed it
	options := &websocket.AcceptOptions{OriginPatterns: []string{site.Host}}
	conn, err := websocket.Accept(c.Writer, c.Request, options)
	if err != nil {
		return // Accept has answered the request.
	}
	h.joinRoom(ctx, conn, roomID, ticket)
}

// joinRoom refuses with close codes after the upgrade: a browser cannot read why a handshake failed.
func (h *Handler) joinRoom(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, ticket string) {
	now := h.Now()
	role, err := VerifyTicket(h.Issuer.Secret, ticket, roomID, now)
	if err != nil {
		slog.InfoContext(ctx, "video_refused",
			"request_id", web.RequestID(ctx),
			"room_id", roomID.String(),
			"code", int(CloseTicketInvalid),
		)
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

// byRoom counts the parsed id, so the same room written differently is still one room.
func byRoom(c *gin.Context) string {
	roomID, _ := pathRoomID(c) // checkRoomRequest has refused a bad id before the limit runs
	return roomID.String()
}

// pathRoomID reads the room id without answering: the socket route refuses a bad id with its own problem.
func pathRoomID(c *gin.Context) (pgtype.UUID, error) {
	var roomID pgtype.UUID
	err := roomID.Scan(c.Param("roomId"))
	return roomID, err
}
