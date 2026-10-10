package video

import (
	"crypto/hmac"
	"crypto/sha1" // coturn defines its REST-API credential as HMAC-SHA1
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// A room takes one connection of each role.
const (
	RoleClient       = "client"
	RolePractitioner = "practitioner"
)

// TicketLifetime is how long a room ticket opens the WebSocket.
const TicketLifetime = 5 * time.Minute

const stunServer = "stun:stun.l.google.com:19302"

// The WebSocket refuses each the same way; they differ for tests and logs.
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

// ICEServer is one entry of RTCPeerConnection's iceServers.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// Issuer signs room tickets. TURNHost is empty in development.
type Issuer struct {
	Secret       []byte
	PublicAPIURL string
	TURNHost     string
	TURNSecret   string
}

// Issue signs a ticket for role in room, with TURN credentials valid until closesAt.
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

// VerifyTicket returns the ticket's role. Nothing in it is trusted before the signature matches.
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

// webSocketURL is the room's signalling address on the API's public origin.
func webSocketURL(publicAPIURL string, room pgtype.UUID) (string, error) {
	u, err := url.Parse(publicAPIURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", errors.New("video: PUBLIC_API_URL is not http or https")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/video/rooms/" + room.String() + "/ws"
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// ICEServers is Google STUN plus, when turnHost is set, our coturn.
func ICEServers(turnHost, secret string, room pgtype.UUID, role string, closesAt time.Time) []ICEServer {
	servers := []ICEServer{{URLs: []string{stunServer}}}
	if turnHost == "" || secret == "" {
		return servers
	}
	// coturn's REST-API username starts with the credential's expiry.
	username := fmt.Sprintf("%d:%s:%s", closesAt.Unix(), room.String(), role)
	return append(servers, ICEServer{
		URLs:       []string{"turn:" + turnHost + "?transport=udp", "turn:" + turnHost + "?transport=tcp"},
		Username:   username,
		Credential: TURNCredential(secret, username),
	})
}

// TURNCredential is coturn's password for username under its static-auth-secret.
func TURNCredential(secret, username string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
