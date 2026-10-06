// Package video owns VetMiMi's own video rooms (ADR-007): one room per
// confirmed online appointment, its join token, the state its join page
// shows, room tickets and TURN credentials.
package video

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// The join window: the page opens 15 minutes before the start and the room
// closes an hour after the end. Both are real durations, added to instants,
// so a daylight-saving change never stretches or shrinks them.
const (
	OpensBefore = 15 * time.Minute
	ClosesAfter = 60 * time.Minute
)

// ModeRoom is the meeting_link_mode in which confirming an online
// appointment creates a room; in manual_link mode Daw Mi pastes a link.
const ModeRoom = "vetmimi_room"

// Room states and the reasons a room ends (docs/data-model.md, "video_rooms").
const (
	StateWaiting   = "waiting"
	StateInSession = "in_session"
	StateEnded     = "ended"

	EndedByCancellation = "appointment_cancelled"
)

// Window is when a room for an appointment from start to end is open.
func Window(start, end time.Time) (opens, closes time.Time) {
	return start.Add(-OpensBefore), end.Add(ClosesAfter)
}

// CreateRoom gives a just-confirmed appointment its room, in the confirming
// transaction, when it is online and mode is ModeRoom; otherwise it does
// nothing. mode is read at confirm time only, so a later switch leaves
// existing rooms working. The token is derived from a fresh seed and only
// its hash is stored. It returns the task that ends the room when its window
// closes.
func CreateRoom(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, mode string,
	now time.Time) ([]platform.Task, error) {
	if appt.Format != "online" || mode != ModeRoom {
		return nil, nil
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	opens, closes := Window(appt.StartsAt, appt.EndsAt)
	room, err := q.InsertVideoRoom(ctx, db.InsertVideoRoomParams{
		AppointmentID: appt.ID,
		JoinTokenSeed: seed,
		JoinTokenHash: platform.HashToken(platform.NewJoinToken(secret, seed)),
		OpensAt:       opens,
		ClosesAt:      closes,
		Now:           now,
	})
	if err != nil {
		return nil, err
	}
	return []platform.Task{closeTask(room)}, nil
}

// MoveRoom moves the window of a rescheduled appointment's room, if it has
// one that has not ended, in the rescheduling transaction. The token stays,
// so the link already emailed keeps working. The task for the new close
// time has a new id; the old one fires, finds the window moved and does
// nothing.
func MoveRoom(ctx context.Context, q db.Querier, appt db.Appointment, now time.Time) ([]platform.Task, error) {
	opens, closes := Window(appt.StartsAt, appt.EndsAt)
	rooms, err := q.MoveVideoRoom(ctx, db.MoveVideoRoomParams{
		AppointmentID: appt.ID, OpensAt: opens, ClosesAt: closes, Now: now,
	})
	var tasks []platform.Task
	for _, r := range rooms {
		tasks = append(tasks, closeTask(r))
	}
	return tasks, err
}

// EndRoom ends a cancelled appointment's room, if it has one, in the
// cancelling transaction. Nobody is connected before the window opens, and
// cancellation is only possible before the start; closing live sockets
// arrives with the hub.
func EndRoom(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, now time.Time) error {
	return q.EndVideoRoom(ctx, db.EndVideoRoomParams{
		AppointmentID: appointmentID,
		EndedReason:   pgtype.Text{String: EndedByCancellation, Valid: true},
		Now:           now,
	})
}

// RoomOf is an appointment's room, or false when it has none.
func RoomOf(ctx context.Context, q db.Querier, appointmentID pgtype.UUID) (db.VideoRoom, bool, error) {
	room, err := q.GetVideoRoomByAppointment(ctx, appointmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VideoRoom{}, false, nil
	}
	return room, err == nil, err
}
