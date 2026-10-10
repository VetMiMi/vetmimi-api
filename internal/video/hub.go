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

// Close codes the join page tells apart (openapi.yaml, connectVideoRoom).
const (
	CloseReplaced      websocket.StatusCode = 4000
	CloseTicketInvalid websocket.StatusCode = 4001
	CloseRoomEnded     websocket.StatusCode = 4002
)

const (
	maxFrame   = 16 << 10
	sendBuffer = 64
	writeLimit = 10 * time.Second
	// checkEvery is how often the hub reads its open rooms back: a room the
	// worker ended, or whose window has closed, has its sockets closed
	// within this long.
	checkEvery = 30 * time.Second
)

// Hub relays signaling between the two participants of each room with open
// connections (ADR-007). It lives in the api process's memory; ADR-005's
// live host runs one, so every participant of a room meets here.
type Hub struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  clock.Now
	// A participant that does not answer a ping within pongWithin is gone.
	pingEvery, pongWithin time.Duration

	mu       sync.Mutex
	rooms    map[[16]byte]*room
	stopping bool
}

type room struct {
	state string
	peers map[string]*peer
}

type peer struct {
	conn   *websocket.Conn
	role   string
	joined bool
	// send holds frames for the writer goroutine, filled under Hub.mu so
	// each participant sees events in the order the hub saw them.
	send chan []byte
}

// NewHub returns an empty hub writing room state to pool.
func NewHub(pool *pgxpool.Pool, log *slog.Logger, now clock.Now) *Hub {
	return &Hub{pool: pool, log: log, now: now, pingEvery: 20 * time.Second, pongWithin: 10 * time.Second,
		rooms: map[[16]byte]*room{}}
}

// Serve runs one participant's accepted connection to roomID until it
// closes. The caller has checked the ticket that gave role and that the
// room, in state, is open. A newer connection for the same role replaces
// this one. Logs carry ids only, never a frame.
func (h *Hub) Serve(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, role, state, requestID string) {
	conn.SetReadLimit(maxFrame)
	log := h.log.With("room_id", roomID.String(), "role", role, "request_id", requestID)
	p := &peer{conn: conn, role: role, send: make(chan []byte, sendBuffer)}
	old, ok := h.add(roomID, state, p)
	if !ok {
		_ = conn.Close(websocket.StatusGoingAway, "server restarting")
		return
	}
	if old != nil {
		log.InfoContext(ctx, "video_replaced")
		go func() { _ = old.conn.Close(CloseReplaced, "replaced by a newer connection for the same role") }()
	}
	log.InfoContext(ctx, "video_connected")

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go p.write(ctx)
	go h.keepAlive(ctx, p)
	code := h.read(ctx, roomID, p, log)
	h.remove(roomID, p)
	log.InfoContext(ctx, "video_disconnected", "code", int(code))
}

// EndRoom closes both participants' sockets with 4002 and forgets the room.
// Callers end the room in PostgreSQL first, so a reconnect is refused.
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
		go func() { _ = p.conn.Close(CloseRoomEnded, "room ended") }()
	}
}

// Run checks the open rooms every checkEvery until ctx is done, then closes
// every socket with 1001 so browsers reconnect to the restarted server.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(checkEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.closeAll()
			return
		case <-t.C:
			h.check(ctx)
		}
	}
}

// check ends the rooms that have ended in PostgreSQL (the worker's
// close-room task) or whose window has closed, even if the task is late.
func (h *Hub) check(ctx context.Context) {
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
		wg.Go(func() { _ = p.conn.Close(websocket.StatusGoingAway, "server restarting") })
	}
	wg.Wait()
}

// add puts p in its role's place, returning the connection it replaces,
// or false once the hub is shutting down.
func (h *Hub) add(roomID pgtype.UUID, state string, p *peer) (*peer, bool) {
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
	old := r.peers[p.role]
	r.peers[p.role] = p
	if old != nil {
		h.sendState(r)
	}
	return old, true
}

// remove takes p out of its room, unless something replaced it already.
func (h *Hub) remove(roomID pgtype.UUID, p *peer) {
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
	h.sendState(r)
}

// read relays p's frames until its socket closes, and returns the close
// code. Anything but a valid frame, or a frame before join, closes it with
// 1008; a frame over maxFrame is closed with 1009 by the library.
func (h *Hub) read(ctx context.Context, roomID pgtype.UUID, p *peer, log *slog.Logger) websocket.StatusCode {
	joined := false
	for {
		typ, frame, err := p.conn.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
		kind := messageType(frame)
		if typ != websocket.MessageText || kind == "" || kind != "join" && !joined {
			_ = p.conn.Close(websocket.StatusPolicyViolation, "message invalid or sent before join")
			return websocket.StatusPolicyViolation
		}
		if kind == "join" {
			joined = true
			h.join(ctx, roomID, p, log)
			continue
		}
		h.relay(roomID, p, frame)
	}
}

// join marks p joined and, once both participants have joined a waiting
// room, puts it in session. Everyone hears the new peer-state.
func (h *Hub) join(ctx context.Context, roomID pgtype.UUID, p *peer, log *slog.Logger) {
	h.mu.Lock()
	p.joined = true
	r := h.rooms[roomID.Bytes]
	start := r != nil && r.state == StateWaiting && bothJoined(r)
	switch {
	case start:
		r.state = StateInSession
	case r != nil:
		h.sendState(r)
	}
	h.mu.Unlock()
	if !start {
		return
	}
	// The row is written before anyone hears in_session.
	_, err := db.New(h.pool).SetVideoRoomInSession(ctx, db.SetVideoRoomInSessionParams{ID: roomID, Now: h.now()})
	if err != nil {
		log.ErrorContext(ctx, "video_room_state_failed", "err", err)
	} else {
		log.InfoContext(ctx, "video_room_state", "state", StateInSession)
	}
	h.mu.Lock()
	if r = h.rooms[roomID.Bytes]; r != nil {
		h.sendState(r)
	}
	h.mu.Unlock()
}

func bothJoined(r *room) bool {
	c, pr := r.peers[RoleClient], r.peers[RolePractitioner]
	return c != nil && c.joined && pr != nil && pr.joined
}

// relay forwards frame unchanged to the other participant, if there is one.
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

// sendState queues peer-state for everyone in r. The caller holds h.mu.
func (h *Hub) sendState(r *room) {
	state := peerState{Type: "peer-state", Client: presence(r.peers[RoleClient]),
		Practitioner: presence(r.peers[RolePractitioner]), Room: r.state}
	frame, _ := json.Marshal(state)
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

// queue hands frame to p's writer; a participant too slow to keep up is
// dropped rather than holding up the room.
func (p *peer) queue(frame []byte) {
	select {
	case p.send <- frame:
	default:
		go p.conn.CloseNow()
	}
}

func (p *peer) write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-p.send:
			wctx, cancel := context.WithTimeout(ctx, writeLimit)
			err := p.conn.Write(wctx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				_ = p.conn.CloseNow()
				return
			}
		}
	}
}

// keepAlive pings p; a ping not answered in time closes the connection,
// which ends its read loop and tells the other participant.
func (h *Hub) keepAlive(ctx context.Context, p *peer) {
	t := time.NewTicker(h.pingEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, h.pongWithin)
			err := p.conn.Ping(pctx)
			cancel()
			if err != nil {
				_ = p.conn.CloseNow()
				return
			}
		}
	}
}
