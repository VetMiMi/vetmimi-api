package video

import (
	"context"
	"time"
)

// Hooks for the external tests: run the hub's periodic check and shutdown
// now, ping on a test's time scale, and run the task handlers directly.

func (h *Hub) Check(ctx context.Context) { h.check(ctx) }

func (h *Hub) CloseAll() { h.closeAll() }

func (h *Hub) PingEvery(every, within time.Duration) { h.pingEvery, h.pongWithin = every, within }

func (t *Tasks) CloseRoom(ctx context.Context, payload []byte) error {
	return t.closeRoom(ctx, payload)
}

func (t *Tasks) SweepRooms(ctx context.Context) error { return t.sweepRooms(ctx, nil) }
