package video_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// dialRoom opens the room's socket through the whole router, which proves the
// upgrade survives the request log and panic recovery.
func dialRoom(t *testing.T, srv *httptest.Server, roomID, ticket, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/video/rooms/" + roomID + "/ws?ticket=" + ticket
	c, res, err := websocket.Dial(context.Background(), u, &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": {origin}},
	})
	if err == nil {
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, res, err
}

func TestHTTP_RoomSocket(t *testing.T) {
	appt, _ := withRoom(t)
	a := videoAPI(t, appt.StartsAt)
	srv := httptest.NewServer(a.Router)
	t.Cleanup(srv.Close)
	room, _, err := video.RoomOf(context.Background(), db.New(pgtest.Pool(t)), appt.ID)
	require.NoError(t, err)
	tk, err := issuer.Issue(room.ID, video.RoleClient, room.ClosesAt, appt.StartsAt)
	require.NoError(t, err)

	c, _, err := dialRoom(t, srv, room.ID.String(), tk.Value, siteOrigin)
	require.NoError(t, err)
	send(t, c, `{"type":"join"}`)
	until(t, c, peerStateWith("client", "connected"))

	_, res, err := dialRoom(t, srv, room.ID.String(), tk.Value, "https://evil.example")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	_, res, err = dialRoom(t, srv, "not-a-uuid", tk.Value, siteOrigin)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)
}
