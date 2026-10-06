package video_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func closeTasks(t *testing.T, now time.Time) *video.Tasks {
	return &video.Tasks{Pool: pgtest.Pool(t), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return now }}
}

func roomRow(t *testing.T, room db.VideoRoom) db.VideoRoom {
	t.Helper()
	r, err := db.New(pgtest.Pool(t)).GetVideoRoom(context.Background(), room.ID)
	require.NoError(t, err)
	return r
}

func TestCloseRoom_EndsOnlyAtItsWindow(t *testing.T) {
	ctx := context.Background()
	appt, _ := withRoom(t)
	room, _, err := video.RoomOf(ctx, db.New(pgtest.Pool(t)), appt.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]string{"room_id": room.ID.String()})
	require.NoError(t, err)

	require.NoError(t, closeTasks(t, room.ClosesAt.Add(-time.Second)).CloseRoom(ctx, payload))
	require.Equal(t, video.StateWaiting, roomRow(t, room).State, "a moved or early task leaves it open")

	require.NoError(t, closeTasks(t, room.ClosesAt).CloseRoom(ctx, payload))
	ended := roomRow(t, room)
	require.Equal(t, video.StateEnded, ended.State)
	require.Equal(t, video.EndedByWindowClosing, ended.EndedReason.String)

	require.NoError(t, closeTasks(t, room.ClosesAt.Add(time.Hour)).CloseRoom(ctx, payload))
	require.True(t, ended.EndedAt.Time.Equal(roomRow(t, room).EndedAt.Time), "a second run changes nothing")
}

func TestSweepRooms_EndsOverdueRooms(t *testing.T) {
	ctx := context.Background()
	appt, _ := withRoom(t)
	room, _, err := video.RoomOf(ctx, db.New(pgtest.Pool(t)), appt.ID)
	require.NoError(t, err)
	require.NoError(t, closeTasks(t, room.ClosesAt).SweepRooms(ctx))
	require.Equal(t, video.EndedByWindowClosing, roomRow(t, room).EndedReason.String)
}

// Daw Mi ending the session and the close task racing on one room: exactly
// one write lands, so the reason is whichever committed first and ended_at
// is never rewritten.
func TestEndAndCloseRace_OneWriteWins(t *testing.T) {
	ctx := context.Background()
	appt, _ := withRoom(t)
	q := db.New(pgtest.Pool(t))
	room, _, err := video.RoomOf(ctx, q, appt.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(map[string]string{"room_id": room.ID.String()})
	require.NoError(t, err)

	var endErr error
	done := make(chan struct{})
	go func() {
		_, endErr = video.EndAsPractitioner(ctx, q, appt.ID, room.ClosesAt)
		close(done)
	}()
	require.NoError(t, closeTasks(t, room.ClosesAt).CloseRoom(ctx, payload))
	<-done
	if endErr == nil {
		require.Equal(t, video.EndedByPractitioner, roomRow(t, room).EndedReason.String)
	} else {
		require.Equal(t, video.EndedByWindowClosing, roomRow(t, room).EndedReason.String)
		var e *apperr.Error
		require.ErrorAs(t, endErr, &e)
		require.Equal(t, apperr.InvalidTransition, e.Code, "Daw Mi's end found the room ended")
	}
}
