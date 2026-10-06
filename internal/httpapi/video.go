package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

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

// StartVideoSession and EndVideoSession arrive with Daw Mi's side of the
// room; until then they answer 501.
func (s *server) StartVideoSession(context.Context, gen.StartVideoSessionRequestObject) (gen.StartVideoSessionResponseObject, error) {
	return notImplemented{}, nil
}

// EndVideoSession: see StartVideoSession.
func (s *server) EndVideoSession(context.Context, gen.EndVideoSessionRequestObject) (gen.EndVideoSessionResponseObject, error) {
	return notImplemented{}, nil
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

type notImplemented struct{}

func (notImplemented) VisitStartVideoSessionResponse(w http.ResponseWriter) error {
	return notImplemented{}.write(w)
}
func (notImplemented) VisitEndVideoSessionResponse(w http.ResponseWriter) error {
	return notImplemented{}.write(w)
}

func (notImplemented) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(http.StatusNotImplemented)
	return json.NewEncoder(w).Encode(map[string]any{
		"type": problemTypePrefix + "not_implemented", "title": "Not implemented",
		"status": http.StatusNotImplemented, "code": "not_implemented",
	})
}
