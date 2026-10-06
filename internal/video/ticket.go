package video

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Participant roles; a room takes one connection of each.
const (
	RoleClient       = "client"
	RolePractitioner = "practitioner"
)

// TicketLifetime is how long a room ticket opens the WebSocket (ADR-007).
const TicketLifetime = 5 * time.Minute

// Why VerifyTicket refuses a ticket. The WebSocket answers each the same;
// they differ for tests and logs.
var (
	ErrTicketSignature = errors.New("video: ticket malformed or not signed by us")
	ErrTicketRoom      = errors.New("video: ticket is for another room")
	ErrTicketExpired   = errors.New("video: ticket expired")
	ErrTicketRole      = errors.New("video: ticket has an unknown role")
)

type ticketPayload struct {
	Room string `json:"room"`
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

// Ticket is what a participant needs to open the room's WebSocket.
type Ticket struct {
	RoomID       pgtype.UUID
	Role         string
	Value        string
	ExpiresAt    time.Time
	WebSocketURL string
	ICEServers   []ICEServer
}

// Issuer signs room tickets and hands out the ICE servers to use with them.
// TURNHost empty (development) leaves TURN out.
type Issuer struct {
	Secret       []byte
	PublicAPIURL string
	TURNHost     string
	TURNSecret   string
}

// Issue signs a ticket for role in room, valid TicketLifetime from now, with
// TURN credentials valid to the room's close.
func (i Issuer) Issue(room pgtype.UUID, role string, closesAt, now time.Time) (Ticket, error) {
	exp := now.Add(TicketLifetime).Truncate(time.Second)
	payload, err := json.Marshal(ticketPayload{Room: room.String(), Role: role, Exp: exp.Unix()})
	if err != nil {
		return Ticket{}, err
	}
	ws, err := webSocketURL(i.PublicAPIURL, room)
	if err != nil {
		return Ticket{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return Ticket{
		RoomID:       room,
		Role:         role,
		Value:        body + "." + base64.RawURLEncoding.EncodeToString(sign(i.Secret, body)),
		ExpiresAt:    exp.UTC(),
		WebSocketURL: ws,
		ICEServers:   ICEServers(i.TURNHost, i.TURNSecret, room, role, closesAt),
	}, nil
}

// VerifyTicket checks a ticket for room at now and returns its role. The
// signature is compared in constant time before anything in it is trusted.
func VerifyTicket(secret []byte, ticket string, room pgtype.UUID, now time.Time) (string, error) {
	body, sig, ok := strings.Cut(ticket, ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if !ok || err != nil || !hmac.Equal(got, sign(secret, body)) {
		return "", ErrTicketSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", ErrTicketSignature
	}
	var p ticketPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", ErrTicketSignature
	}
	switch {
	case p.Room != room.String():
		return "", ErrTicketRoom
	case now.Unix() >= p.Exp:
		return "", ErrTicketExpired
	case p.Role != RoleClient && p.Role != RolePractitioner:
		return "", ErrTicketRole
	}
	return p.Role, nil
}

func sign(secret []byte, body string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// webSocketURL is the room's signaling address on the API's public origin,
// without the ticket.
func webSocketURL(publicAPIURL string, room pgtype.UUID) (string, error) {
	u, err := url.Parse(publicAPIURL)
	if err != nil {
		return "", err
	}
	u.Scheme = map[string]string{"https": "wss", "http": "ws"}[u.Scheme]
	if u.Scheme == "" {
		return "", errors.New("video: PUBLIC_API_URL is not http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/video/rooms/" + room.String() + "/ws"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}
