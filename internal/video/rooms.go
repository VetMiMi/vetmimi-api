// Package video runs VetMiMi's own video rooms. Confirming an online
// appointment creates its room; the join link and the admin hand out tickets,
// and the Hub relays WebRTC signalling between the two participants.
package video

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

// Real durations, so daylight saving never stretches a room's window.
const (
	OpensBefore = 15 * time.Minute
	ClosesAfter = 60 * time.Minute
)

// ModeRoom is the meeting_link_mode in which confirming creates a room.
const ModeRoom = "vetmimi_room"

const (
	StateWaiting   = "waiting"
	StateInSession = "in_session"
	StateEnded     = "ended"

	EndedByCancellation  = "appointment_cancelled"
	EndedByPractitioner  = "practitioner"
	EndedByWindowClosing = "window_closed"
)

var errNoOpenRoom = apperr.New(apperr.InvalidTransition, "This appointment has no open video room.")

// ErrRoomOver means the room is unknown, ended or past its window.
var ErrRoomOver = errors.New("video: room over")

// Window is when the room for an appointment from start to end is open.
func Window(start, end time.Time) (opens, closes time.Time) {
	return start.Add(-OpensBefore), end.Add(ClosesAfter)
}

// CreateRoom gives a just-confirmed online appointment its room and returns the task that ends it.
func CreateRoom(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, mode string,
	now time.Time) ([]queue.Task, error) {
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
		JoinTokenHash: tokens.Hash(tokens.Join(secret, seed)),
		OpensAt:       opens,
		ClosesAt:      closes,
		Now:           now,
	})
	if err != nil {
		return nil, err
	}
	return []queue.Task{closeTask(room)}, nil
}

// MoveRoom moves an open room to the new times. The token stays, so the emailed link still works.
func MoveRoom(ctx context.Context, q db.Querier, appt db.Appointment, now time.Time) ([]queue.Task, error) {
	opens, closes := Window(appt.StartsAt, appt.EndsAt)
	rooms, err := q.MoveVideoRoom(ctx, db.MoveVideoRoomParams{
		AppointmentID: appt.ID, OpensAt: opens, ClosesAt: closes, Now: now,
	})
	if err != nil {
		return nil, err
	}
	var tasks []queue.Task
	for _, r := range rooms {
		tasks = append(tasks, closeTask(r))
	}
	return tasks, nil
}

// EndRoom ends the open room and returns its id, not Valid when none was open.
// After commit the caller closes its sockets with Hub.EndRoom.
func EndRoom(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, reason string,
	now time.Time) (pgtype.UUID, error) {
	room, err := endRoom(ctx, q, appointmentID, reason, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, nil
	}
	return room.ID, err
}

// EndAsPractitioner ends the room; the appointment keeps its status until Daw Mi marks it.
func EndAsPractitioner(ctx context.Context, q db.Querier, appointmentID pgtype.UUID,
	now time.Time) (db.VideoRoom, error) {
	if _, err := appointmentStatus(ctx, q, appointmentID); err != nil {
		return db.VideoRoom{}, err
	}
	room, err := endRoom(ctx, q, appointmentID, EndedByPractitioner, now)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VideoRoom{}, errNoOpenRoom
	}
	return room, err
}

func endRoom(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, reason string,
	now time.Time) (db.VideoRoom, error) {
	return q.EndVideoRoom(ctx, db.EndVideoRoomParams{
		AppointmentID: appointmentID,
		EndedReason:   pgtype.Text{String: reason, Valid: true},
		Now:           now,
	})
}

// Actions are the video actions the admin may offer for the room.
func Actions(room db.VideoRoom, confirmed bool, now time.Time) []string {
	if room.State == StateEnded || now.Before(room.OpensAt) {
		return nil
	}
	var actions []string
	if confirmed && now.Before(room.ClosesAt) {
		actions = append(actions, "start_video")
	}
	return append(actions, "end_video")
}

// RoomOf returns an appointment's room, or false when it has none.
func RoomOf(ctx context.Context, q db.Querier, appointmentID pgtype.UUID) (db.VideoRoom, bool, error) {
	room, err := q.GetVideoRoomByAppointment(ctx, appointmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VideoRoom{}, false, nil
	}
	if err != nil {
		return db.VideoRoom{}, false, err
	}
	return room, true, nil
}

// OpenRoom returns a room that still accepts participants, or ErrRoomOver.
func OpenRoom(ctx context.Context, q db.Querier, id pgtype.UUID, now time.Time) (db.VideoRoom, error) {
	room, err := q.GetVideoRoom(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VideoRoom{}, ErrRoomOver
	}
	if err != nil {
		return db.VideoRoom{}, err
	}
	if isOver(room, now) {
		return db.VideoRoom{}, ErrRoomOver
	}
	return room, nil
}

func isOver(room db.VideoRoom, now time.Time) bool {
	return room.State == StateEnded || !now.Before(room.ClosesAt)
}
