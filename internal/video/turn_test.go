package video_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/video"
)

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
