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

// queue hands frame to p's writer, dropping a participant too slow to keep up.
func (p *peer) queue(frame []byte) {
	select {
	case p.send <- frame:
	default:
		go p.conn.CloseNow()
	}
}

// closeInBackground closes p without waiting for the close handshake.
func (p *peer) closeInBackground(code websocket.StatusCode, reason string) {
	go func() { _ = p.conn.Close(code, reason) }()
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

// keepAlive closes p when a ping goes unanswered.
func (p *peer) keepAlive(ctx context.Context, every, within time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, within)
			err := p.conn.Ping(pctx)
			cancel()
			if err != nil {
				_ = p.conn.CloseNow()
				return
			}
		}
	}
}
