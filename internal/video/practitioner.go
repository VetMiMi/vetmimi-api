package video

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

var (
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
	errNoOpenRoom          = apperr.New(apperr.InvalidTransition, "This appointment has no open video room.")
)

// JoinAsPractitioner issues Daw Mi's ticket for an appointment's room, only
// when the admin would offer start_video: confirmed, with a room that has
// not ended, inside its window.
func JoinAsPractitioner(ctx context.Context, q db.Querier, issuer Issuer, appointmentID pgtype.UUID,
	now time.Time) (Ticket, error) {
	status, err := appointmentStatus(ctx, q, appointmentID)
	if err != nil {
		return Ticket{}, err
	}
	room, ok, err := RoomOf(ctx, q, appointmentID)
	if err != nil {
		return Ticket{}, err
	}
	if !ok || !slices.Contains(Actions(room, status == "confirmed", now), "start_video") {
		return Ticket{}, errNotReady
	}
	return issuer.Issue(room.ID, RolePractitioner, room.ClosesAt, now)
}

// EndAsPractitioner ends an appointment's room because Daw Mi ended the
// session. The appointment keeps its status: it is completed only when she
// marks it. The caller closes the sockets with Hub.EndRoom.
func EndAsPractitioner(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, now time.Time) (db.VideoRoom, error) {
	if _, err := appointmentStatus(ctx, q, appointmentID); err != nil {
		return db.VideoRoom{}, err
	}
	room, err := q.EndVideoRoom(ctx, db.EndVideoRoomParams{
		AppointmentID: appointmentID,
		EndedReason:   pgtype.Text{String: EndedByPractitioner, Valid: true},
		Now:           now,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.VideoRoom{}, errNoOpenRoom
	}
	return room, err
}

func appointmentStatus(ctx context.Context, q db.Querier, id pgtype.UUID) (string, error) {
	status, err := q.GetAppointmentStatus(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errAppointmentNotFound
	}
	return status, err
}
