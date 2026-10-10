package video_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func TestTicket_RoundTrip(t *testing.T) {
	room := roomID(t, "8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b")
	now := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	ticket, err := issuer.Issue(room, video.RoleClient, now.Add(2*time.Hour), now)
	require.NoError(t, err)
	require.Equal(t, now.Add(5*time.Minute), ticket.ExpiresAt)
	require.Equal(t, "wss://api.vetmimi.example/video/rooms/8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b/ws", ticket.WebSocketURL)

	role, err := video.VerifyTicket(issuer.Secret, ticket.Value, room, now.Add(5*time.Minute-time.Second))
	require.NoError(t, err)
	require.Equal(t, video.RoleClient, role)
}

func TestTicket_Refusals(t *testing.T) {
	room := roomID(t, "8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b")
	other := roomID(t, "1b2c3d4e-0000-4000-8000-000000000001")
	now := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	ticket, err := issuer.Issue(room, video.RolePractitioner, now, now)
	require.NoError(t, err)
	admin, err := issuer.Issue(room, "admin", now, now)
	require.NoError(t, err)
	body, sig, _ := strings.Cut(ticket.Value, ".")
	flipped := []byte(sig)
	flipped[0] ^= 1

	for name, c := range map[string]struct {
		ticket string
		room   pgtype.UUID
		at     time.Time
		want   error
	}{
		"tampered signature":    {body + "." + string(flipped), room, now, video.ErrTicketSignature},
		"signed by another key": {mustIssue(t, video.Issuer{Secret: []byte("another key")}, room, now), room, now, video.ErrTicketSignature},
		"no signature":          {body, room, now, video.ErrTicketSignature},
		"another room":          {ticket.Value, other, now, video.ErrTicketRoom},
		"expired":               {ticket.Value, room, ticket.ExpiresAt, video.ErrTicketExpired},
		"unknown role":          {admin.Value, room, now, video.ErrTicketRole},
	} {
		_, err := video.VerifyTicket(issuer.Secret, c.ticket, c.room, c.at)
		require.ErrorIs(t, err, c.want, name)
	}
}

func mustIssue(t *testing.T, i video.Issuer, room pgtype.UUID, now time.Time) string {
	t.Helper()
	i.PublicAPIURL = "https://api.vetmimi.example"
	ticket, err := i.Issue(room, video.RoleClient, now, now)
	require.NoError(t, err)
	return ticket.Value
}

// The vector is coturn's REST-API scheme computed independently:
// printf '%s' '1791043200:8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b:client' |
// openssl dgst -sha1 -hmac 'turn-secret' -binary | base64
func TestICEServers_CoturnCredential(t *testing.T) {
	room := roomID(t, "8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b")
	closes := time.Unix(1791043200, 0)
	got := video.ICEServers("turn.vetmimi.example:3478", "turn-secret", room, video.RoleClient, closes)
	require.Equal(t, []video.ICEServer{
		{URLs: []string{"stun:stun.l.google.com:19302"}},
		{
			URLs: []string{"turn:turn.vetmimi.example:3478?transport=udp",
				"turn:turn.vetmimi.example:3478?transport=tcp"},
			Username:   "1791043200:8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b:client",
			Credential: "0TMML8ZTpbki3yp2msjo5S7prwk=",
		},
	}, got)
}

func TestICEServers_STUNOnlyWithoutTURN(t *testing.T) {
	room := roomID(t, "8f14e45f-ceea-4e7a-9b1f-2c3d4e5f6a7b")
	got := video.ICEServers("", "", room, video.RoleClient, time.Now())
	require.Equal(t, []video.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}, got)
}
