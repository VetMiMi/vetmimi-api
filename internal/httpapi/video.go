package httpapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// GetPublicSession shows the join page's state for a join token. An
// unknown token answers the same 404 as any other link.
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

// CreateRoomTicket gives the visitor a ticket for the room while the session
// is ready. The log names the room only; the token is as good as a password.
func (s *server) CreateRoomTicket(ctx context.Context, req gen.CreateRoomTicketRequestObject) (gen.CreateRoomTicketResponseObject, error) {
	t, err := video.JoinAsClient(ctx, db.New(s.Pool), s.issuer(), req.Token, s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "room_ticket_issued", "request_id", RequestID(ctx), "room_id", t.RoomID.String(),
		"role", t.Role)
	return gen.CreateRoomTicket201JSONResponse(ticketView(t)), nil
}

// StartVideoSession gives Daw Mi her ticket for an appointment's room while
// the admin would offer start_video.
func (s *server) StartVideoSession(ctx context.Context, req gen.StartVideoSessionRequestObject) (gen.StartVideoSessionResponseObject, error) {
	t, err := video.JoinAsPractitioner(ctx, db.New(s.Pool), s.issuer(), uuid(req.AppointmentId), s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "room_ticket_issued", "request_id", RequestID(ctx), "room_id", t.RoomID.String(),
		"role", t.Role)
	return gen.StartVideoSession201JSONResponse(ticketView(t)), nil
}

// EndVideoSession ends the room and closes both sockets. The appointment
// stays as it is until Daw Mi marks it completed or a no-show.
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
