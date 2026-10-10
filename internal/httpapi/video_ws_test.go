package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

const siteOrigin = "https://vetmimi.example"

// dialRoom opens the room's WebSocket through the whole router, middleware
// included, which is what proves the upgrade survives the request log and
// panic recovery.
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

func mustDial(t *testing.T, srv *httptest.Server, roomID, ticket string) *websocket.Conn {
	t.Helper()
	c, _, err := dialRoom(t, srv, roomID, ticket, siteOrigin)
	require.NoError(t, err)
	return c
}

// readUntil reads c until a frame contains want.
func readUntil(t *testing.T, c *websocket.Conn, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, frame, err := c.Read(ctx)
		require.NoError(t, err)
		if strings.Contains(string(frame), want) {
			return
		}
	}
}

func closedWith(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestVideoSession_DawMiAndTheClientMeetAndSheEndsIt(t *testing.T) {
	a := newAuthAPI(t)
	start := a.clock.at.Add(5 * time.Minute).Truncate(time.Minute).UTC()
	appt, room, joinToken := withRoom(t, start)
	admin := insertSession(t, a.clock.at, "booking_admin")
	path := appointmentsURL + "/" + appt.ID.String()

	detail := decoded(t, http.StatusOK, a.send(http.MethodGet, path, admin))
	require.Subset(t, detail["allowedActions"], []any{"start_video", "end_video"})

	mine := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, path+"/video-session", admin, ""))
	require.Equal(t, "practitioner", mine["role"])
	require.Equal(t, room.ID.String(), mine["roomId"])
	theirs := decoded(t, http.StatusCreated, a.sendPublic(http.MethodPost, "/public/sessions/"+joinToken+"/ticket", ""))

	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)
	practitioner := mustDial(t, srv, room.ID.String(), mine["ticket"].(string))
	client := mustDial(t, srv, room.ID.String(), theirs["ticket"].(string))
	require.NoError(t, practitioner.Write(context.Background(), websocket.MessageText, []byte(`{"type":"join"}`)))
	require.NoError(t, client.Write(context.Background(), websocket.MessageText, []byte(`{"type":"join"}`)))
	readUntil(t, client, `"room":"in_session"`)
	require.NoError(t, practitioner.Write(context.Background(), websocket.MessageText, []byte(`{"type":"offer","sdp":"v=0"}`)))
	readUntil(t, client, `"sdp":"v=0"`)

	ended := decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/video-session/end", admin, ""))
	require.Equal(t, "ended", ended["state"])
	require.Equal(t, video.CloseRoomEnded, closedWith(t, client))
	require.Equal(t, video.CloseRoomEnded, closedWith(t, practitioner))
	row, err := db.New(pgtest.Pool(t)).GetVideoRoom(context.Background(), room.ID)
	require.NoError(t, err)
	require.Equal(t, video.EndedByPractitioner, row.EndedReason.String)

	refused(t, http.StatusConflict, "invalid_transition", a.sendJSON(http.MethodPost, path+"/video-session/end", admin, ""))
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendJSON(http.MethodPost, path+"/video-session", admin, ""))
	c := mustDial(t, srv, room.ID.String(), mine["ticket"].(string))
	require.Equal(t, video.CloseRoomEnded, closedWith(t, c), "an ended room takes no one in")

	a.clock.at = start.Add(10 * time.Minute)
	detail = decoded(t, http.StatusOK, a.send(http.MethodGet, path, admin))
	require.Equal(t, "confirmed", detail["status"], "not completed until Daw Mi marks it")
	require.Equal(t, []any{"complete", "no_show", "set_note", "mark_communicated"}, detail["allowedActions"])
}

func TestVideoSession_StartRefusals(t *testing.T) {
	a := newAuthAPI(t)
	admin := insertSession(t, a.clock.at, "booking_admin")
	early, _, _ := withRoom(t, a.clock.at.Add(2*time.Hour).Truncate(time.Minute))
	requireForbidden(t, a.sendJSON(http.MethodPost, appointmentsURL+"/"+early.ID.String()+"/video-session",
		insertSession(t, a.clock.at, "content_editor"), ""))
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendJSON(http.MethodPost, appointmentsURL+"/"+early.ID.String()+"/video-session", admin, ""))

	// An appointment confirmed in manual_link mode has no room.
	svc, err := db.New(pgtest.Pool(t)).GetServiceBySlug(context.Background(), "individual-art-therapy")
	require.NoError(t, err)
	manual := bookOnce(t, svc.ID.String(), a.clock.at.Add(5*time.Minute).Truncate(time.Minute), booking.Confirmed)
	path := appointmentsURL + "/" + manual.ID.String()
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPost, path+"/video-session", admin, ""))
	refused(t, http.StatusConflict, "invalid_transition", a.sendJSON(http.MethodPost, path+"/video-session/end", admin, ""))

	unknown := appointmentsURL + "/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10/video-session"
	refused(t, http.StatusNotFound, "not_found", a.sendJSON(http.MethodPost, unknown, admin, ""))
	refused(t, http.StatusNotFound, "not_found", a.sendJSON(http.MethodPost, unknown+"/end", admin, ""))
}

func TestVideoSocket_Refusals(t *testing.T) {
	a := newAuthAPI(t)
	start := a.clock.at.Add(5 * time.Minute).Truncate(time.Minute).UTC()
	_, room, _ := withRoom(t, start)
	_, other, _ := withRoom(t, start.Add(24*time.Hour))
	issuer := video.Issuer{Secret: []byte("test signing secret, 32 bytes ok"), PublicAPIURL: "https://api.vetmimi.example"}
	ticket := func(r pgtype.UUID, at time.Time) string {
		tk, err := issuer.Issue(r, video.RoleClient, start.Add(2*time.Hour), at)
		require.NoError(t, err)
		return tk.Value
	}
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)
	id, valid := room.ID.String(), ticket(room.ID, a.clock.at)

	_, res, err := dialRoom(t, srv, id, valid, "https://evil.example")
	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, res.StatusCode)
	_, res, err = dialRoom(t, srv, "not-a-uuid", valid, siteOrigin)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, res.StatusCode)

	for name, tk := range map[string]string{
		"another room's": ticket(other.ID, a.clock.at),
		"expired":        ticket(room.ID, a.clock.at.Add(-video.TicketLifetime)),
		"forged":         valid[:len(valid)-2] + "AA",
	} {
		require.Equal(t, video.CloseTicketInvalid, closedWith(t, mustDial(t, srv, id, tk)), name)
	}

	_, err = pgtest.Pool(t).Exec(context.Background(), "UPDATE video_rooms SET state = 'ended' WHERE id = $1", other.ID)
	require.NoError(t, err)
	require.Equal(t, video.CloseRoomEnded, closedWith(t, mustDial(t, srv, other.ID.String(), ticket(other.ID, a.clock.at))))

	// Three upgrades above counted against this room; the limit is 20 a minute.
	for range 17 {
		_ = mustDial(t, srv, id, valid).CloseNow()
	}
	_, res, err = dialRoom(t, srv, id, valid, siteOrigin)
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, res.StatusCode)
}

func TestVideoSocket_CancellingClosesTheRoom(t *testing.T) {
	a := newAuthAPI(t)
	start := a.clock.at.Add(10 * time.Minute).Truncate(time.Minute).UTC()
	appt, room, joinToken := withRoom(t, start)
	theirs := decoded(t, http.StatusCreated, a.sendPublic(http.MethodPost, "/public/sessions/"+joinToken+"/ticket", ""))
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)
	client := mustDial(t, srv, room.ID.String(), theirs["ticket"].(string))
	require.NoError(t, client.Write(context.Background(), websocket.MessageText, []byte(`{"type":"join"}`)))
	readUntil(t, client, "peer-state")

	admin := insertSession(t, a.clock.at, "booking_admin")
	body, err := json.Marshal(map[string]any{"version": appt.Version, "notifyVisitor": false})
	require.NoError(t, err)
	decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, appointmentsURL+"/"+appt.ID.String()+"/cancel", admin, string(body)))
	require.Equal(t, video.CloseRoomEnded, closedWith(t, client))
}
