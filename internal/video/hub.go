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

// Hub relays signalling between the two participants of each room. It lives
// in memory, which works because the single api process holds every room.
type Hub struct {
	pool       *pgxpool.Pool
	log        *slog.Logger
	now        clock.Now
	pingEvery  time.Duration
	pongWithin time.Duration

	mu       sync.Mutex // guards everything below, every room, and each peer's joined flag
	rooms    map[[16]byte]*liveRoom
	stopping bool
}

// liveRoom is a room with sockets open in this process.
type liveRoom struct {
	state string
	peers map[string]*peer // by role
}

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

func NewHub(pool *pgxpool.Pool, log *slog.Logger, now clock.Now) *Hub {
	return &Hub{
		pool:       pool,
		log:        log,
		now:        now,
		pingEvery:  20 * time.Second,
		pongWithin: 10 * time.Second,
		rooms:      map[[16]byte]*liveRoom{},
	}
}

// Serve runs one participant's connection until it closes. The caller has
// checked the ticket and that the room is open. Logs never carry a frame.
func (hub *Hub) Serve(ctx context.Context, conn *websocket.Conn, roomID pgtype.UUID, role, state, requestID string) {
	conn.SetReadLimit(maxFrame) // the library closes a bigger frame with 1009
	log := hub.log.With("room_id", roomID.String(), "role", role, "request_id", requestID)
	participant := &peer{
		conn: conn,
		role: role,
		send: make(chan []byte, sendBuffer),
	}

	replaced, ok := hub.enter(roomID, state, participant)
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
	go participant.write(ctx)
	go participant.keepAlive(ctx, hub.pingEvery, hub.pongWithin)
	code := hub.readLoop(ctx, roomID, participant, log)
	hub.leave(roomID, participant)
	log.InfoContext(ctx, "video_disconnected", "code", int(code))
}

// EndRoom closes the room's sockets. End it in PostgreSQL first, so a reconnect is refused.
func (hub *Hub) EndRoom(roomID pgtype.UUID) {
	hub.mu.Lock()
	room := hub.rooms[roomID.Bytes]
	delete(hub.rooms, roomID.Bytes)
	hub.mu.Unlock()
	if room == nil {
		return
	}
	hub.log.Info("video_room_state", "room_id", roomID.String(), "state", StateEnded)
	for _, participant := range room.peers {
		participant.closeInBackground(CloseRoomEnded, "room ended")
	}
}

// Run ends finished rooms until ctx is done, then closes every socket.
func (hub *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			hub.closeAll()
			return
		case <-ticker.C:
			hub.endFinishedRooms(ctx)
		}
	}
}

// readLoop handles the participant's frames until its socket closes and returns the close code.
func (hub *Hub) readLoop(ctx context.Context, roomID pgtype.UUID, participant *peer, log *slog.Logger) websocket.StatusCode {
	joined := false
	for {
		frameType, frame, err := participant.conn.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
		kind := messageType(frame)
		if frameType != websocket.MessageText || kind == "" || (kind != "join" && !joined) {
			_ = participant.conn.Close(websocket.StatusPolicyViolation, "message invalid or sent before join")
			return websocket.StatusPolicyViolation
		}
		if kind == "join" {
			joined = true
			if hub.join(roomID, participant) {
				hub.markInSession(ctx, roomID, log)
			}
			continue
		}
		hub.relay(roomID, participant, frame)
	}
}

// enter puts the participant in its role's place and returns the connection it replaces;
// false once the hub is shutting down.
func (hub *Hub) enter(roomID pgtype.UUID, state string, participant *peer) (replaced *peer, ok bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	if hub.stopping {
		return nil, false
	}
	room := hub.rooms[roomID.Bytes]
	if room == nil {
		room = &liveRoom{state: state, peers: map[string]*peer{}}
		hub.rooms[roomID.Bytes] = room
	}
	replaced = room.peers[participant.role]
	room.peers[participant.role] = participant
	if replaced != nil {
		room.sendState()
	}
	return replaced, true
}

// join marks the participant joined and reports whether both have now joined a waiting room.
func (hub *Hub) join(roomID pgtype.UUID, participant *peer) (startsSession bool) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	participant.joined = true
	room := hub.rooms[roomID.Bytes]
	if room == nil {
		return false
	}
	if room.state == StateWaiting && room.bothJoined() {
		room.state = StateInSession
		return true
	}
	room.sendState()
	return false
}

// markInSession saves in_session before either participant hears it.
func (hub *Hub) markInSession(ctx context.Context, roomID pgtype.UUID, log *slog.Logger) {
	params := db.SetVideoRoomInSessionParams{
		ID:  roomID,
		Now: hub.now(),
	}
	_, err := db.New(hub.pool).SetVideoRoomInSession(ctx, params)
	if err != nil {
		log.ErrorContext(ctx, "video_room_state_failed", "err", err)
	} else {
		log.InfoContext(ctx, "video_room_state", "state", StateInSession)
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	room := hub.rooms[roomID.Bytes]
	if room != nil {
		room.sendState()
	}
}

// relay forwards frame unchanged to the other participant. "leave" is relayed
// too: a participant leaves the room only when its socket closes.
func (hub *Hub) relay(roomID pgtype.UUID, from *peer, frame []byte) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	room := hub.rooms[roomID.Bytes]
	if room == nil {
		return
	}
	for role, participant := range room.peers {
		if role != from.role {
			participant.queue(frame)
		}
	}
}

// leave takes the participant out of its room, unless a newer connection replaced it.
func (hub *Hub) leave(roomID pgtype.UUID, participant *peer) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	room := hub.rooms[roomID.Bytes]
	if room == nil || room.peers[participant.role] != participant {
		return
	}
	delete(room.peers, participant.role)
	if len(room.peers) == 0 {
		delete(hub.rooms, roomID.Bytes)
		return
	}
	room.sendState()
}

// endFinishedRooms ends rooms the worker ended or whose window closed, even if its task is late.
func (hub *Hub) endFinishedRooms(ctx context.Context) {
	hub.mu.Lock()
	ids := make([]pgtype.UUID, 0, len(hub.rooms))
	for id := range hub.rooms {
		ids = append(ids, pgtype.UUID{Bytes: id, Valid: true})
	}
	hub.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	rooms, err := db.New(hub.pool).ListVideoRooms(ctx, ids)
	if err != nil {
		hub.log.ErrorContext(ctx, "video_check_failed", "err", err)
		return
	}
	now := hub.now()
	for _, room := range rooms {
		if isOver(room, now) {
			hub.EndRoom(room.ID)
		}
	}
}

// closeAll closes every socket with CloseRestarting and refuses new ones.
func (hub *Hub) closeAll() {
	hub.mu.Lock()
	hub.stopping = true
	var participants []*peer
	for _, room := range hub.rooms {
		for _, participant := range room.peers {
			participants = append(participants, participant)
		}
	}
	hub.rooms = map[[16]byte]*liveRoom{}
	hub.mu.Unlock()

	var waitGroup sync.WaitGroup
	for _, participant := range participants {
		waitGroup.Go(func() { _ = participant.conn.Close(CloseRestarting, "server restarting") })
	}
	waitGroup.Wait()
}

func (room *liveRoom) bothJoined() bool {
	client := room.peers[RoleClient]
	practitioner := room.peers[RolePractitioner]
	return client != nil && client.joined && practitioner != nil && practitioner.joined
}

// sendState tells everyone in the room who is connected and how the room stands.
func (room *liveRoom) sendState() {
	message := peerState{
		Type:         "peer-state",
		Client:       presence(room.peers[RoleClient]),
		Practitioner: presence(room.peers[RolePractitioner]),
		Room:         room.state,
	}
	frame, _ := json.Marshal(message) // a struct of strings cannot fail to marshal
	for _, participant := range room.peers {
		participant.queue(frame)
	}
}

func presence(participant *peer) string {
	if participant == nil {
		return "disconnected"
	}
	return "connected"
}
