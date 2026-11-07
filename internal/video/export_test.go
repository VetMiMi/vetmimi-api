package video

import (
	"context"
	"time"
)

func (h *Hub) EndFinishedRooms(ctx context.Context) { h.endFinishedRooms(ctx) }

func (h *Hub) CloseAll() { h.closeAll() }

func (h *Hub) PingEvery(every, within time.Duration) { h.pingEvery, h.pongWithin = every, within }
