package video_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// lockedBuffer collects the hub's log lines from its goroutines.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type hubTest struct {
	hub  *video.Hub
	room db.VideoRoom
	url  string
	logs *lockedBuffer

	mu  sync.Mutex
	now time.Time
}

// newHubTest serves a fresh room's hub behind a bare WebSocket endpoint;
// the ticket and room checks in front of it are httpapi's, tested there.
func newHubTest(t *testing.T) *hubTest {
	t.Helper()
	appt, _ := withRoom(t)
	room, _, err := video.RoomOf(context.Background(), db.New(pgtest.Pool(t)), appt.ID)
	require.NoError(t, err)
	ht := &hubTest{room: room, logs: &lockedBuffer{}, now: appt.StartsAt}
	ht.hub = video.NewHub(pgtest.Pool(t), slog.New(slog.NewJSONHandler(ht.logs, nil)), ht.clock)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		ht.hub.Serve(r.Context(), conn, room.ID, r.URL.Query().Get("role"), room.State, "req-1")
	}))
	t.Cleanup(srv.Close)
	ht.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return ht
}

func (ht *hubTest) clock() time.Time {
	ht.mu.Lock()
	defer ht.mu.Unlock()
	return ht.now
}

func (ht *hubTest) dial(t *testing.T, role string) *websocket.Conn {
	t.Helper()
	c, _, err := websocket.Dial(context.Background(), ht.url+"?role="+role, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.CloseNow() })
	return c
}

func send(t *testing.T, c *websocket.Conn, frame string) {
	t.Helper()
	require.NoError(t, c.Write(context.Background(), websocket.MessageText, []byte(frame)))
}

// until reads frames from c until one satisfies ok, and returns it.
func until(t *testing.T, c *websocket.Conn, ok func(frame []byte) bool) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		_, frame, err := c.Read(ctx)
		require.NoError(t, err)
		if ok(frame) {
			return frame
		}
	}
}

func peerStateWith(field, value string) func([]byte) bool {
	return func(frame []byte) bool {
		var m map[string]string
		_ = json.Unmarshal(frame, &m)
		return m["type"] == "peer-state" && m[field] == value
	}
}

// closeCode reads c until the server closes it and returns the code.
func closeCode(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	return closeCodeWithin(c, 5*time.Second)
}

// closeCodeWithin is closeCode, or -1 if c is still open after d.
func closeCodeWithin(c *websocket.Conn, d time.Duration) websocket.StatusCode {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// both connects and joins a client and a practitioner and waits for the
// room to be in session.
func (ht *hubTest) both(t *testing.T) (client, practitioner *websocket.Conn) {
	t.Helper()
	client, practitioner = ht.dial(t, video.RoleClient), ht.dial(t, video.RolePractitioner)
	send(t, client, `{"type":"join"}`)
	send(t, practitioner, `{"type":"join"}`)
	until(t, client, peerStateWith("room", "in_session"))
	until(t, practitioner, peerStateWith("room", "in_session"))
	return client, practitioner
}

func TestHub_RelaysBetweenTheTwoParticipants(t *testing.T) {
	ht := newHubTest(t)
	client, practitioner := ht.both(t)

	room, err := db.New(pgtest.Pool(t)).GetVideoRoom(context.Background(), ht.room.ID)
	require.NoError(t, err)
	require.Equal(t, video.StateInSession, room.State)
	require.True(t, room.StartedAt.Time.Equal(ht.clock()))

	offer := `{"type":"offer","sdp":"v=0 private-sdp"}`
	send(t, practitioner, offer)
	require.Equal(t, offer, string(until(t, client, func([]byte) bool { return true })), "byte for byte")
	ice := `{"type":"ice","candidate":{"candidate":"candidate:private-ip","sdpMid":"0","sdpMLineIndex":0}}`
	send(t, client, ice)
	require.Equal(t, ice, string(until(t, practitioner, func([]byte) bool { return true })))

	send(t, client, `{"type":"leave"}`)
	until(t, practitioner, func(f []byte) bool { return string(f) == `{"type":"leave"}` })
	require.NoError(t, client.Close(websocket.StatusNormalClosure, ""))
	until(t, practitioner, peerStateWith("client", "disconnected"))

	logs := ht.logs.String()
	require.Contains(t, logs, "video_room_state")
	require.NotContains(t, logs, "private", "frames are never logged")
}

func TestHub_NewConnectionReplacesTheSameRole(t *testing.T) {
	ht := newHubTest(t)
	first, practitioner := ht.both(t)
	ht.dial(t, video.RoleClient)
	require.Equal(t, video.CloseReplaced, closeCode(t, first))
	until(t, practitioner, peerStateWith("client", "connected"))
}

func TestHub_ConcurrentConnectionsLeaveOne(t *testing.T) {
	ht := newHubTest(t)
	conns := make([]*websocket.Conn, 20)
	var wg sync.WaitGroup
	for i := range conns {
		wg.Go(func() {
			c, _, err := websocket.Dial(context.Background(), ht.url+"?role=client", nil)
			if err == nil {
				conns[i] = c
			}
		})
	}
	wg.Wait()
	codes := make([]websocket.StatusCode, len(conns))
	for i, c := range conns {
		require.NotNil(t, c)
		wg.Go(func() { codes[i] = closeCodeWithin(c, 2*time.Second) })
	}
	wg.Wait()
	replaced := 0
	for _, code := range codes {
		if code == video.CloseReplaced {
			replaced++
		}
	}
	require.Equal(t, 19, replaced, "one connection stays open")
}

func TestHub_ClosesInvalidFrames(t *testing.T) {
	ht := newHubTest(t)
	early := ht.dial(t, video.RoleClient)
	send(t, early, `{"type":"offer","sdp":"v=0"}`)
	require.Equal(t, websocket.StatusPolicyViolation, closeCode(t, early), "offer before join")

	for frame, want := range map[string]websocket.StatusCode{
		`{"type":"offer"}`:          websocket.StatusPolicyViolation,
		`{"type":"join","extra":1}`: websocket.StatusPolicyViolation,
		`{"type":"hello"}`:          websocket.StatusPolicyViolation,
		`{"type":"answer","sdp":"` + strings.Repeat("a", 17<<10) + `"}`: websocket.StatusMessageTooBig,
	} {
		c := ht.dial(t, video.RoleClient)
		send(t, c, `{"type":"join"}`)
		_ = c.Write(context.Background(), websocket.MessageText, []byte(frame))
		require.Equal(t, want, closeCode(t, c), frame[:min(len(frame), 30)])
	}
}

func TestHub_EndRoomClosesBoth(t *testing.T) {
	ht := newHubTest(t)
	client, practitioner := ht.both(t)
	ht.hub.EndRoom(ht.room.ID)
	require.Equal(t, video.CloseRoomEnded, closeCode(t, client))
	require.Equal(t, video.CloseRoomEnded, closeCode(t, practitioner))
}

func TestHub_ClosesAtWindowEnd(t *testing.T) {
	ht := newHubTest(t)
	client, practitioner := ht.both(t)
	ht.hub.Check(context.Background())
	ht.mu.Lock()
	ht.now = ht.room.ClosesAt
	ht.mu.Unlock()
	send(t, client, `{"type":"ice","candidate":{"candidate":""}}`)
	until(t, practitioner, func(f []byte) bool { return strings.Contains(string(f), `"ice"`) })

	ht.hub.Check(context.Background())
	require.Equal(t, video.CloseRoomEnded, closeCode(t, client), "even before the worker ends the row")
	require.Equal(t, video.CloseRoomEnded, closeCode(t, practitioner))
}

func TestHub_ClosesRoomsTheWorkerEnded(t *testing.T) {
	ht := newHubTest(t)
	client, _ := ht.both(t)
	_, err := pgtest.Pool(t).Exec(context.Background(), "UPDATE video_rooms SET state = 'ended' WHERE id = $1", ht.room.ID)
	require.NoError(t, err)
	ht.hub.Check(context.Background())
	require.Equal(t, video.CloseRoomEnded, closeCode(t, client))
}

func TestHub_DropsAParticipantThatStopsAnsweringPings(t *testing.T) {
	ht := newHubTest(t)
	ht.hub.PingEvery(50*time.Millisecond, 500*time.Millisecond)
	practitioner := ht.dial(t, video.RolePractitioner)
	send(t, practitioner, `{"type":"join"}`)
	until(t, practitioner, peerStateWith("client", "disconnected"))
	silent := ht.dial(t, video.RoleClient)
	send(t, silent, `{"type":"join"}`) // and never reads, so never answers a ping
	until(t, practitioner, peerStateWith("client", "connected"))
	until(t, practitioner, peerStateWith("client", "disconnected"))
}

func TestHub_ShutdownClosesEverySocketForReconnect(t *testing.T) {
	ht := newHubTest(t)
	client, practitioner := ht.both(t)
	ht.hub.CloseAll()
	require.Equal(t, websocket.StatusGoingAway, closeCode(t, client))
	require.Equal(t, websocket.StatusGoingAway, closeCode(t, practitioner))
	require.Equal(t, websocket.StatusGoingAway, closeCode(t, ht.dial(t, video.RoleClient)), "no new connections")
}
