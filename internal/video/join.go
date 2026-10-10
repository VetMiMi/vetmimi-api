package video

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

// What a join page may show.
const (
	TooEarly = "too_early"
	Ready    = "ready"
	Expired  = "expired"
	Ended    = "ended"
)

var (
	// Same wording as every other bad link, so a token never reveals whether an appointment exists.
	errSessionNotFound     = apperr.New(apperr.NotFound, "This link is not valid or has expired.")
	errNotReady            = apperr.New(apperr.ActionNotAllowed, "This session cannot be joined now.")
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
)

// Session is a room as the holder of its join link sees it: no private details.
type Session struct {
	RoomID                              pgtype.UUID
	State                               string
	OpensAt, ClosesAt, StartsAt, EndsAt time.Time
	Timezone, Locale                    string
	ServiceSlug, ServiceName            string
	confirmed                           bool
}

// PublicState is what the join page shows; a passed window is Expired even if the room ended.
func PublicState(roomState string, opens, closes, now time.Time) string {
	switch {
	case !now.Before(closes):
		return Expired
	case roomState == StateEnded:
		return Ended
	case now.Before(opens):
		return TooEarly
	}
	return Ready
}

// FindSession reads the room a join token names.
func FindSession(ctx context.Context, q db.Querier, token string, now time.Time) (Session, error) {
	row, err := q.GetVideoSessionByTokenHash(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, errSessionNotFound
	}
	if err != nil {
		return Session{}, err
	}
	return Session{
		RoomID:      row.ID,
		State:       PublicState(row.State, row.OpensAt, row.ClosesAt, now),
		OpensAt:     row.OpensAt.UTC(),
		ClosesAt:    row.ClosesAt.UTC(),
		StartsAt:    row.StartsAt.UTC(),
		EndsAt:      row.EndsAt.UTC(),
		Timezone:    row.Timezone,
		Locale:      row.Locale,
		ServiceSlug: row.ServiceSlug,
		ServiceName: inLocale(row.ServiceName, row.Locale),
		confirmed:   row.AppointmentStatus == "confirmed",
	}, nil
}

// JoinAsClient issues the visitor's ticket while the session is ready.
func JoinAsClient(ctx context.Context, q db.Querier, issuer Issuer, token string, now time.Time) (Ticket, error) {
	s, err := FindSession(ctx, q, token, now)
	if err != nil {
		return Ticket{}, err
	}
	if s.State != Ready || !s.confirmed {
		return Ticket{}, errNotReady
	}
	return issuer.Issue(s.RoomID, RoleClient, s.ClosesAt, now)
}

// JoinAsPractitioner issues Daw Mi's ticket when the admin would offer start_video.
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

func appointmentStatus(ctx context.Context, q db.Querier, id pgtype.UUID) (string, error) {
	status, err := q.GetAppointmentStatus(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errAppointmentNotFound
	}
	return status, err
}

func inLocale(raw json.RawMessage, locale string) string {
	var l settings.Localized
	if json.Unmarshal(raw, &l) != nil {
		return ""
	}
	if locale == "my" && l.My != "" {
		return l.My
	}
	return l.En
}
