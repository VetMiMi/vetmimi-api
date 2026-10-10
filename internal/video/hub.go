package video

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Close codes the join page tells apart.
const (
	CloseReplaced      websocket.StatusCode = 4000 // a newer connection took this role
	CloseTicketInvalid websocket.StatusCode = 4001
	CloseRoomEnded     websocket.StatusCode = 4002
	CloseRestarting                         = websocket.StatusGoingAway // 1001: the browser reconnects
)

const (
	maxFrame   = 16 << 10
	sendBuffer = 64
	writeLimit = 10 * time.Second
	checkEvery = 30 * time.Second // how soon sockets close after the worker ends a room
)

// Hub relays signalling between the two participants of each room. It lives
// in memory, which works because the single api process holds every room.
type Hub struct {
	pool                  *pgxpool.Pool
	log                   *slog.Logger
	now                   clock.Now
	pingEvery, pongWithin time.Duration

	mu       sync.Mutex // guards everything below, every room, and each peer's joined flag
	rooms    map[[16]byte]*room
	stopping bool
}

type room struct {
	state string
	peers map[string]*peer // by role
}

func NewHub(pool *pgxpool.Pool, log *slog.Logger, now clock.Now) *Hub {
	return &Hub{
		pool:       pool,
		log:        log,
		now:        now,
		pingEvery:  20 * time.Second,
		pongWithin: 10 * time.Second,
		rooms:      map[[16]byte]*room{},
	}
}

// Serve runs one participant's connection until it closes. The caller has
// checked the ticket and that the room is open. Logs never carry a frame.
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, role, state, requestID string) {
	conn.SetReadLimit(maxFrame) // the library closes a bigger frame with 1009
	log := h.log.With("room_id", roomID.String(), "role", role, "request_id", requestID)
	p := &peer{conn: conn, role: role, send: make(chan []byte, sendBuffer)}

	replaced, ok := h.enter(roomID, state, p)
	if !ok {
		_ = conn.Close(CloseRestarting, "server restarting")
		return
	}
	if replaced != nil {
		log.InfoContext(ctx, "video_replaced")
		replaced.closeInBackground(CloseReplaced, "replaced by a newer connection for the same role")
	}
	log.InfoContext(ctx, "video_connected")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.write(ctx)
	go p.keepAlive(ctx, h.pingEvery, h.pongWithin)
	code := h.readLoop(ctx, roomID, p, log)
	h.leave(roomID, p)
	log.InfoContext(ctx, "video_disconnected", "code", int(code))
}

// readLoop handles p's frames until its socket closes and returns the close code.
func (h *Hub) readLoop(ctx context.Context, roomID pgtype.UUID, p *peer, log *slog.Logger) websocket.StatusCode {
	joined := false
	for {
		typ, frame, err := p.conn.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
		kind := messageType(frame)
		if typ != websocket.MessageText || kind == "" || (kind != "join" && !joined) {
			_ = p.conn.Close(websocket.StatusPolicyViolation, "message invalid or sent before join")
			return websocket.StatusPolicyViolation
		}
		if kind == "join" {
			joined = true
			if h.join(roomID, p) {
				h.startSession(ctx, roomID, log)
			}
			continue
		}
		h.relay(roomID, p, frame)
	}
}

// enter puts p in its role's place and returns the connection it replaces;
// false once the hub is shutting down.
func (h *Hub) enter(roomID pgtype.UUID, state string, p *peer) (replaced *peer, ok bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping {
		return nil, false
	}
	r := h.rooms[roomID.Bytes]
	if r == nil {
		r = &room{state: state, peers: map[string]*peer{}}
		h.rooms[roomID.Bytes] = r
	}
	replaced = r.peers[p.role]
	r.peers[p.role] = p
	if replaced != nil {
		r.sendState()
	}
	return replaced, true
}

// join marks p joined and reports whether both have now joined a waiting room.
func (h *Hub) join(roomID pgtype.UUID, p *peer) (startsSession bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p.joined = true
	r := h.rooms[roomID.Bytes]
	if r == nil {
		return false
	}
	if r.state == StateWaiting && r.bothJoined() {
		r.state = StateInSession
		return true
	}
	r.sendState()
	return false
}

// startSession saves in_session before either participant hears it.
func (h *Hub) startSession(ctx context.Context, roomID pgtype.UUID, log *slog.Logger) {
	_, err := db.New(h.pool).SetVideoRoomInSession(ctx, db.SetVideoRoomInSessionParams{ID: roomID, Now: h.now()})
	if err != nil {
		log.ErrorContext(ctx, "video_room_state_failed", "err", err)
	} else {
		log.InfoContext(ctx, "video_room_state", "state", StateInSession)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[roomID.Bytes]; r != nil {
		r.sendState()
	}
}

// relay forwards frame unchanged to the other participant. "leave" is relayed
// too: a participant leaves the room only when its socket closes.
func (h *Hub) relay(roomID pgtype.UUID, from *peer, frame []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[roomID.Bytes]
	if r == nil {
		return
	}
	for role, p := range r.peers {
		if role != from.role {
			p.queue(frame)
		}
	}
}

// leave takes p out of its room, unless a newer connection replaced it.
func (h *Hub) leave(roomID pgtype.UUID, p *peer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[roomID.Bytes]
	if r == nil || r.peers[p.role] != p {
		return
	}
	delete(r.peers, p.role)
	if len(r.peers) == 0 {
		delete(h.rooms, roomID.Bytes)
		return
	}
	r.sendState()
}

// EndRoom closes the room's sockets. End it in PostgreSQL first, so a reconnect is refused.
func (h *Hub) EndRoom(roomID pgtype.UUID) {
	h.mu.Lock()
	r := h.rooms[roomID.Bytes]
	delete(h.rooms, roomID.Bytes)
	h.mu.Unlock()
	if r == nil {
		return
	}
	h.log.Info("video_room_state", "room_id", roomID.String(), "state", StateEnded)
	for _, p := range r.peers {
		p.closeInBackground(CloseRoomEnded, "room ended")
	}
}

// Run ends finished rooms until ctx is done, then closes every socket.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			return
		case <-t.C:
			h.endFinishedRooms(ctx)
		}
	}
}

// endFinishedRooms ends rooms the worker ended or whose window closed, even if its task is late.
func (h *Hub) endFinishedRooms(ctx context.Context) {
	h.mu.Lock()
	ids := make([]pgtype.UUID, 0, len(h.rooms))
	for id := range h.rooms {
		ids = append(ids, pgtype.UUID{Bytes: id, Valid: true})
	}
	h.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	rooms, err := db.New(h.pool).ListVideoRooms(ctx, ids)
	if err != nil {
		h.log.ErrorContext(ctx, "video_check_failed", "err", err)
		return
	}
	now := h.now()
	for _, r := range rooms {
		if r.State == StateEnded || !now.Before(r.ClosesAt) {
			h.EndRoom(r.ID)
		}
	}
}

// closeAll closes every socket with CloseRestarting and refuses new ones.
func (h *Hub) closeAll() {
	h.mu.Lock()
	h.stopping = true
	var peers []*peer
	for _, r := range h.rooms {
		for _, p := range r.peers {
			peers = append(peers, p)
		}
	}
	h.rooms = map[[16]byte]*room{}
	h.mu.Unlock()

	var wg sync.WaitGroup
	for _, p := range peers {
		wg.Go(func() { _ = p.conn.Close(CloseRestarting, "server restarting") })
	}
	wg.Wait()
}

func (r *room) bothJoined() bool {
	c, pr := r.peers[RoleClient], r.peers[RolePractitioner]
	return c != nil && c.joined && pr != nil && pr.joined
}

// sendState tells everyone in r who is connected and how the room stands.
func (r *room) sendState() {
	frame, _ := json.Marshal(peerState{
		Type:         "peer-state",
		Client:       presence(r.peers[RoleClient]),
		Practitioner: presence(r.peers[RolePractitioner]),
		Room:         r.state,
	})
	for _, p := range r.peers {
		p.queue(frame)
	}
}

func presence(p *peer) string {
	if p == nil {
		return "disconnected"
	}
	return "connected"
}
