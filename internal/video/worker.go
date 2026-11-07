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

type closeRoomPayload struct {
	RoomID string `json:"room_id"`
}

// Tasks runs the video task handlers in the worker.
type Tasks struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	Now  clock.Now
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskCloseRoom, t.CloseRoom)
	w.Handle(TaskSweepRooms, t.SweepRooms)
	w.Every("@every 5m", TaskSweepRooms)
}

// CloseRoom ends the room if it is still open and its window has passed.
// It is safe to repeat: an ended room is left as it is.
func (t *Tasks) CloseRoom(ctx context.Context, payload []byte) error {
	var p closeRoomPayload
	err := json.Unmarshal(payload, &p)
	if err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}
	var roomID pgtype.UUID
	err = roomID.Scan(p.RoomID)
	if err != nil {
		return fmt.Errorf("video: close-room payload: %w", err)
	}

	params := db.CloseVideoRoomIfDueParams{
		ID:  roomID,
		Now: t.Now(),
	}
	err = db.New(t.Pool).CloseVideoRoomIfDue(ctx, params)
	if err != nil {
		return err
	}

	t.Log.InfoContext(ctx, "video room close checked", "room_id", p.RoomID)
	return nil
}

// SweepRooms ends every room past its window. It is safe to repeat: only open rooms are ended.
func (t *Tasks) SweepRooms(ctx context.Context, _ []byte) error {
	ended, err := db.New(t.Pool).CloseOverdueVideoRooms(ctx, t.Now())
	if err != nil {
		return err
	}

	t.Log.InfoContext(ctx, "video rooms swept", "ended", ended)
	return nil
}

func closeRoomTask(room db.VideoRoom) queue.Task {
	return queue.Task{
		Type:    TaskCloseRoom,
		Payload: closeRoomPayload{RoomID: room.ID.String()},
		// A moved room gets a second task; the old one finds the window moved and does nothing.
		ID:        fmt.Sprintf("room:%s:%d", room.ID.String(), room.ClosesAt.Unix()),
		ProcessAt: room.ClosesAt,
	}
}
