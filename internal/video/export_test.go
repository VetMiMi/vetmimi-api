package video

import (
	"context"
	"time"
)

func (h *Hub) EndFinishedRooms(ctx context.Context) { h.endFinishedRooms(ctx) }

func (h *Hub) CloseAll() { h.closeAll() }

func (h *Hub) PingEvery(every, within time.Duration) { h.pingEvery, h.pongWithin = every, within }

func (t *Tasks) CloseRoom(ctx context.Context, payload []byte) error {
	return t.closeRoom(ctx, payload)
}

func (t *Tasks) SweepRooms(ctx context.Context) error { return t.sweepRooms(ctx, nil) }
