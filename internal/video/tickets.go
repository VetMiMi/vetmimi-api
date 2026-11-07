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

// Ticket is what a participant needs to open the room's WebSocket.
type Ticket struct {
	RoomID       pgtype.UUID `json:"roomId"`
	Role         string      `json:"role"`
	Value        string      `json:"ticket"`
	ExpiresAt    time.Time   `json:"ticketExpiresAt"`
	WebSocketURL string      `json:"websocketUrl"`
	ICEServers   []ICEServer `json:"iceServers"`
}

// ICEServer is one entry of RTCPeerConnection's iceServers.
type ICEServer struct {
	URLs       []string `json:"urls"`
	Username   string   `json:"username,omitempty"`
	Credential string   `json:"credential,omitempty"`
}

// Issuer signs room tickets. TURNHost is empty in development.
type Issuer struct {
	Secret       []byte
	PublicAPIURL string
	TURNHost     string
	TURNSecret   string
}

type ticketPayload struct {
	Room string `json:"room"`
	Role string `json:"role"`
	Exp  int64  `json:"exp"`
}

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

// Issue signs a ticket for role in room, with TURN credentials valid until closesAt.
func (issuer Issuer) Issue(room pgtype.UUID, role string, closesAt, now time.Time) (Ticket, error) {
	expiresAt := now.Add(TicketLifetime).Truncate(time.Second)
	payload := ticketPayload{
		Room: room.String(),
		Role: role,
		Exp:  expiresAt.Unix(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return Ticket{}, err
	}
	socketURL, err := webSocketURL(issuer.PublicAPIURL, room)
	if err != nil {
		return Ticket{}, err
	}
	body := base64.RawURLEncoding.EncodeToString(raw)
	ticket := Ticket{
		RoomID:       room,
		Role:         role,
		Value:        body + "." + base64.RawURLEncoding.EncodeToString(sign(issuer.Secret, body)),
		ExpiresAt:    expiresAt.UTC(),
		WebSocketURL: socketURL,
		ICEServers:   ICEServers(issuer.TURNHost, issuer.TURNSecret, room, role, closesAt),
	}
	return ticket, nil
}

// VerifyTicket returns the ticket's role. Nothing in it is trusted before the signature matches.
func VerifyTicket(secret []byte, ticket string, room pgtype.UUID, now time.Time) (string, error) {
	body, encodedSignature, ok := strings.Cut(ticket, ".")
	signature, err := base64.RawURLEncoding.DecodeString(encodedSignature)
	if !ok || err != nil || !hmac.Equal(signature, sign(secret, body)) {
		return "", ErrTicketSignature
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return "", ErrTicketSignature
	}
	var payload ticketPayload
	err = json.Unmarshal(raw, &payload)
	if err != nil {
		return "", ErrTicketSignature
	}
	switch {
	case payload.Room != room.String():
		return "", ErrTicketRoom
	case now.Unix() >= payload.Exp:
		return "", ErrTicketExpired
	case payload.Role != RoleClient && payload.Role != RolePractitioner:
		return "", ErrTicketRole
	}
	return payload.Role, nil
}

// ICEServers is Google STUN plus, when turnHost is set, our coturn.
func ICEServers(turnHost, secret string, room pgtype.UUID, role string, closesAt time.Time) []ICEServer {
	servers := []ICEServer{{URLs: []string{stunServer}}}
	if turnHost == "" || secret == "" {
		return servers
	}
	// coturn's REST-API username starts with the credential's expiry.
	username := fmt.Sprintf("%d:%s:%s", closesAt.Unix(), room.String(), role)
	turn := ICEServer{
		URLs:       []string{"turn:" + turnHost + "?transport=udp", "turn:" + turnHost + "?transport=tcp"},
		Username:   username,
		Credential: TURNCredential(secret, username),
	}
	return append(servers, turn)
}

// TURNCredential is coturn's password for username under its static-auth-secret.
func TURNCredential(secret, username string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func sign(secret []byte, body string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(body))
	return mac.Sum(nil)
}

// webSocketURL is the room's signalling address on the API's public origin.
func webSocketURL(publicAPIURL string, room pgtype.UUID) (string, error) {
	address, err := url.Parse(publicAPIURL)
	if err != nil {
		return "", err
	}
	switch address.Scheme {
	case "https":
		address.Scheme = "wss"
	case "http":
		address.Scheme = "ws"
	default:
		return "", errors.New("video: PUBLIC_API_URL is not http or https")
	}
	address.Path = strings.TrimRight(address.Path, "/") + "/video/rooms/" + room.String() + "/ws"
	address.RawQuery = ""
	address.Fragment = ""
	return address.String(), nil
}
