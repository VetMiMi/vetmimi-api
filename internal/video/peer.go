package video

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

// peer is one participant's connection to the Hub.
type peer struct {
	conn   *websocket.Conn
	role   string
	joined bool
	// Filled under Hub.mu, so each participant hears events in the order the hub saw them.
	send chan []byte
}

// queue hands frame to the participant's writer, dropping a participant too slow to keep up.
func (participant *peer) queue(frame []byte) {
	select {
	case participant.send <- frame:
	default:
		go participant.conn.CloseNow()
	}
}

// closeInBackground closes the connection without waiting for the close handshake.
func (participant *peer) closeInBackground(code websocket.StatusCode, reason string) {
	go func() { _ = participant.conn.Close(code, reason) }()
}

func (participant *peer) write(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case frame := <-participant.send:
			writeCtx, cancel := context.WithTimeout(ctx, writeLimit)
			err := participant.conn.Write(writeCtx, websocket.MessageText, frame)
			cancel()
			if err != nil {
				_ = participant.conn.CloseNow()
				return
			}
		}
	}
}

// keepAlive closes the connection when a ping goes unanswered.
func (participant *peer) keepAlive(ctx context.Context, every, within time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, within)
			err := participant.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				_ = participant.conn.CloseNow()
				return
			}
		}
	}
}
