package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// GetPublicSession answers an unknown token with the same 404 as any other link.
func (s *server) GetPublicSession(ctx context.Context, req gen.GetPublicSessionRequestObject) (gen.GetPublicSessionResponseObject, error) {
	v, err := video.FindSession(ctx, db.New(s.Pool), req.Token, s.Now())
	if err != nil {
		return nil, err
	}
	return gen.GetPublicSession200JSONResponse{
		State:    gen.PublicSessionStateState(v.State),
		OpensAt:  v.OpensAt,
		ClosesAt: v.ClosesAt,
		StartsAt: v.StartsAt,
		EndsAt:   v.EndsAt,
		Timezone: v.Timezone,
		Service:  gen.PublicServiceRef{Slug: v.ServiceSlug, Name: v.ServiceName},
		Locale:   gen.Locale(v.Locale),
	}, nil
}

// CreateRoomTicket logs the room only: the token is as good as a password.
func (s *server) CreateRoomTicket(ctx context.Context, req gen.CreateRoomTicketRequestObject) (gen.CreateRoomTicketResponseObject, error) {
	t, err := video.JoinAsClient(ctx, db.New(s.Pool), s.issuer(), req.Token, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "room_ticket_issued", "request_id", RequestID(ctx), "room_id", t.RoomID.String(),
		"role", t.Role)
	return gen.CreateRoomTicket201JSONResponse(ticketView(t)), nil
}

// StartVideoSession gives Daw Mi her ticket while the admin offers start_video.
func (s *server) StartVideoSession(ctx context.Context, req gen.StartVideoSessionRequestObject) (gen.StartVideoSessionResponseObject, error) {
	t, err := video.JoinAsPractitioner(ctx, db.New(s.Pool), s.issuer(), uuid(req.AppointmentId), s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "room_ticket_issued", "request_id", RequestID(ctx), "room_id", t.RoomID.String(),
		"role", t.Role)
	return gen.StartVideoSession201JSONResponse(ticketView(t)), nil
}

// EndVideoSession closes both sockets. The appointment keeps its status until
// Daw Mi marks it completed or a no-show.
func (s *server) EndVideoSession(ctx context.Context, req gen.EndVideoSessionRequestObject) (gen.EndVideoSessionResponseObject, error) {
	room, err := video.EndAsPractitioner(ctx, db.New(s.Pool), uuid(req.AppointmentId), s.Now())
	if err != nil {
		return nil, err
	}
	s.Hub.EndRoom(room.ID)
	s.Log.InfoContext(ctx, "video_session_ended", "request_id", RequestID(ctx), "room_id", room.ID.String())
	return gen.EndVideoSession200JSONResponse(*videoRoomView(room)), nil
}

func (s *server) issuer() video.Issuer {
	return video.Issuer{Secret: s.SigningSecret, PublicAPIURL: s.PublicAPIURL, TURNHost: s.TURNHost,
		TURNSecret: s.TURNSecret}
}

func ticketView(t video.Ticket) gen.RoomTicket {
	out := gen.RoomTicket{
		RoomId:          openapi_types.UUID(t.RoomID.Bytes),
		Role:            gen.RoomTicketRole(t.Role),
		Ticket:          t.Value,
		TicketExpiresAt: t.ExpiresAt,
		WebsocketUrl:    t.WebSocketURL,
		IceServers:      make([]gen.IceServer, len(t.ICEServers)),
	}
	for i, ice := range t.ICEServers {
		out.IceServers[i] = gen.IceServer{Urls: ice.URLs, Username: nonEmpty(ice.Username),
			Credential: nonEmpty(ice.Credential)}
	}
	return out
}

// roomSocketPattern is mounted outside the generated server, so the handler
// does its own origin, rate-limit and ticket checks.
const roomSocketPattern = "/video/rooms/{roomId}/ws"

// connectVideoRoom opens a participant's signalling WebSocket. What can be
// refused before the upgrade is answered as a problem.
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
	s.joinRoom(r.Context(), conn, roomID, ticket)
}

// joinRoom checks the ticket and the room after the upgrade and refuses with
// close codes, because a browser cannot read why a handshake failed. Nothing
// is relayed before both pass, and the ticket is never logged.
func (s *server) joinRoom(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, ticket string) {
	now := s.Now()
	role, err := video.VerifyTicket(s.SigningSecret, ticket, roomID, now)
	if err != nil {
		s.Log.InfoContext(ctx, "video_refused", "request_id", RequestID(ctx), "room_id", roomID.String(),
			"code", int(video.CloseTicketInvalid))
		_ = conn.Close(video.CloseTicketInvalid, "ticket invalid or expired")
		return
	}
	room, err := video.OpenRoom(ctx, db.New(s.Pool), roomID, now)
	if errors.Is(err, video.ErrRoomOver) {
		_ = conn.Close(video.CloseRoomEnded, "room ended")
		return
	}
	if err != nil {
		s.Log.ErrorContext(ctx, "video_room_read_failed", "request_id", RequestID(ctx), "err", err)
		_ = conn.Close(websocket.StatusInternalError, "unexpected failure")
		return
	}
	s.Hub.Serve(ctx, conn, roomID, role, room.State, RequestID(ctx))
}
