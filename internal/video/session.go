package video

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// What a join page may show (PublicSessionState.state).
const (
	TooEarly = "too_early"
	Ready    = "ready"
	Expired  = "expired"
	Ended    = "ended"
)

// Session is a room as the holder of its join link sees it: times and the
// service, never the visitor or any note.
type Session struct {
	RoomID                              pgtype.UUID
	State                               string
	OpensAt, ClosesAt, StartsAt, EndsAt time.Time
	Timezone, Locale                    string
	ServiceSlug, ServiceName            string
	confirmed                           bool
}

var (
	// errSessionNotFound reads the same as every other link's 404, so a token
	// never reveals whether an appointment exists (Booking & Admin UX §7).
	errSessionNotFound = apperr.New(apperr.NotFound, "This link is not valid or has expired.")
	errNotReady        = apperr.New(apperr.ActionNotAllowed, "This session cannot be joined now.")
)

// PublicState is what the join page shows for a room in roomState with the
// window [opens, closes) at now. A passed window is expired even if the room
// ended earlier.
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

// FindSession reads the room a join token names. The token is looked up
// only by its hash; an unknown one is not_found.
func FindSession(ctx context.Context, q db.Querier, token string, now time.Time) (Session, error) {
	row, err := q.GetVideoSessionByTokenHash(ctx, platform.HashToken(token))
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

// JoinAsClient issues the visitor's ticket for the room token names, only
// while the session is ready and the appointment confirmed.
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
