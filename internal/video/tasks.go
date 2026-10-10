package video

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

// TaskSweepRooms ends any room whose TaskCloseRoom was lost.
const (
	TaskCloseRoom  = "video:close-room"
	TaskSweepRooms = "video:sweep-rooms"
)

type closePayload struct {
	RoomID string `json:"room_id"`
}

func closeTask(r db.VideoRoom) queue.Task {
	return queue.Task{
		Type:    TaskCloseRoom,
		Payload: closePayload{RoomID: r.ID.String()},
		// A moved room gets a second task; the old one finds the window moved and does nothing.
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

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskCloseRoom, t.closeRoom)
	w.Handle(TaskSweepRooms, t.sweepRooms)
	w.Every("@every 5m", TaskSweepRooms)
}

// closeRoom ends the room if it is still open and its window has passed.
func (t *Tasks) closeRoom(ctx context.Context, payload []byte) error {
	var p closePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}
	var id pgtype.UUID
	if err := id.Scan(p.RoomID); err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}
	err := db.New(t.Pool).CloseVideoRoomIfDue(ctx, db.CloseVideoRoomIfDueParams{ID: id, Now: t.Now()})
	if err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "video room close checked", "room_id", p.RoomID)
	return nil
}

func (t *Tasks) sweepRooms(ctx context.Context, _ []byte) error {
	n, err := db.New(t.Pool).CloseOverdueVideoRooms(ctx, t.Now())
	if err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "video rooms swept", "ended", n)
	return nil
}
