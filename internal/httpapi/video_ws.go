package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// roomSocketPattern is connectVideoRoom's route. It is excluded from the
// generated server, so it does its own origin, rate-limit and ticket checks.
const roomSocketPattern = "/video/rooms/{roomId}/ws"

// connectVideoRoom opens a participant's signaling WebSocket. What can be
// refused before the upgrade is answered as a problem; the ticket and the
// room are checked after it and refused with close codes, because a
// browser cannot read the status of a refused handshake. Nothing is relayed
// before both pass. The ticket never reaches a log.
func (s *server) connectVideoRoom(w http.ResponseWriter, r *http.Request) {
	site, err := url.Parse(s.SiteURL)
	if err != nil || site.Host == "" || r.Header.Get("Origin") != site.Scheme+"://"+site.Host {
		writeProblem(w, apperr.New(apperr.Forbidden, "Video rooms open only from the VetMiMi site."))
		return
	}
	var roomID pgtype.UUID
	ticket := r.URL.Query().Get("ticket")
	if roomID.Scan(chi.URLParam(r, "roomId")) != nil || ticket == "" || len(ticket) > 1000 {
		writeProblem(w, apperr.New(apperr.InvalidRequest, "A room id and a ticket are required."))
		return
	}
	if !s.RateLimits.AllowRoomUpgrade(w, r, roomID.String()) {
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{site.Host}})
	if err != nil {
		return // Accept has answered the request.
	}
	ctx := r.Context()
	now := s.Now()
	role, err := video.VerifyTicket(s.SigningSecret, ticket, roomID, now)
	if err != nil {
		s.Log.InfoContext(ctx, "video_refused", "request_id", RequestID(ctx), "room_id", roomID.String(),
			"code", int(video.CloseTicketInvalid))
		_ = conn.Close(video.CloseTicketInvalid, "ticket invalid or expired")
		return
	}
	room, err := db.New(s.Pool).GetVideoRoom(ctx, roomID)
	switch {
	case errors.Is(err, pgx.ErrNoRows) || err == nil && (room.State == video.StateEnded || !now.Before(room.ClosesAt)):
		_ = conn.Close(video.CloseRoomEnded, "room ended")
		return
	case err != nil:
		s.Log.ErrorContext(ctx, "video_room_read_failed", "request_id", RequestID(ctx), "err", err)
		_ = conn.Close(websocket.StatusInternalError, "unexpected failure")
		return
	}
	s.Hub.Serve(ctx, conn, roomID, role, room.State, RequestID(ctx))
}
