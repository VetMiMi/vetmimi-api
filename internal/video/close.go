package video

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// TaskCloseRoom ends a room when its window closes (docs/architecture.md,
// "Background jobs").
const TaskCloseRoom = "video:close-room"

type closePayload struct {
	RoomID string `json:"room_id"`
}

// closeTask runs at the room's close time. The id names that time, so a
// moved room gets a new task beside the old one instead of colliding.
func closeTask(r db.VideoRoom) platform.Task {
	return platform.Task{
		Type:      TaskCloseRoom,
		Payload:   closePayload{RoomID: r.ID.String()},
		ID:        fmt.Sprintf("room:%s:%d", r.ID.String(), r.ClosesAt.Unix()),
		ProcessAt: r.ClosesAt,
	}
}

// Tasks runs the video task handlers in the worker.
type Tasks struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  clock.Now
}

// Register adds the video handlers to w.
func (t *Tasks) Register(w *platform.Worker) {
	w.Handle(TaskCloseRoom, t.closeRoom)
}

// closeRoom ends the room if it is still open and its window has passed; a
// room moved later or already ended is left alone.
func (t *Tasks) closeRoom(ctx context.Context, payload []byte) error {
	var p closePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}
	var id pgtype.UUID
	if err := id.Scan(p.RoomID); err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}
	now := t.Now()
	if err := db.New(t.Pool).CloseVideoRoomIfDue(ctx, db.CloseVideoRoomIfDueParams{ID: id, Now: now}); err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "video room close checked", "room_id", p.RoomID)
	return nil
}
