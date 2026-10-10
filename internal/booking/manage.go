package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

// Managed is what the holder of a management link may see: the appointment
// and what they may do with it, never the visitor's details or any note.
type Managed struct {
	Reference               string
	Status                  Status
	ServiceSlug             string
	ServiceName             string
	StartsAt, EndsAt        time.Time
	DurationMinutes         int
	Timezone, Format        string
	CanCancel               bool
	CanRequestReschedule    bool
	CancellationNoticeHours int
	LateIfCancelledNow      bool
	RescheduleRequested     bool
}

var (
	// errLinkNotFound is the same for an unknown token and an expired one, so
	// a link never reveals whether an appointment exists (Booking & Admin UX
	// §7).
	errLinkNotFound   = apperr.New(apperr.NotFound, "This link is not valid or has expired.")
	errLinkNotAllowed = apperr.New(apperr.ActionNotAllowed,
		"This appointment can no longer be changed through its link.")
)

// GetManaged reads the appointment a management link names. A token is
// looked up only by its hash. The link works until the appointment is final
// and over.
func GetManaged(ctx context.Context, q db.Querier, token string, now time.Time) (Managed, error) {
	row, err := q.GetManagedAppointment(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && linkExpired(Status(row.Status), row.EndsAt, now) {
		return Managed{}, errLinkNotFound
	}
	if err != nil {
		return Managed{}, err
	}
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Managed{}, err
	}
	requested, err := q.RescheduleRequestOpen(ctx, row.ID)
	if err != nil {
		return Managed{}, err
	}
	status := Status(row.Status)
	notice := time.Duration(cur.CancellationNoticeHours) * time.Hour
	changeable := actionable(status, row.StartsAt, now)
	return Managed{
		Reference:               row.Reference,
		Status:                  status,
		ServiceSlug:             row.ServiceSlug,
		ServiceName:             inLocale(row.ServiceName, row.Locale),
		StartsAt:                row.StartsAt.UTC(),
		EndsAt:                  row.EndsAt.UTC(),
		DurationMinutes:         int(row.DurationMinutes),
		Timezone:                row.Timezone,
		Format:                  row.Format,
		CanCancel:               changeable,
		CanRequestReschedule:    changeable,
		CancellationNoticeHours: cur.CancellationNoticeHours,
		LateIfCancelledNow:      changeable && late(row.StartsAt, now, notice),
		RescheduleRequested:     requested,
	}, nil
}

func linkExpired(s Status, endsAt, now time.Time) bool {
	return Terminal(s) && endsAt.Before(now)
}

// actionable reports whether a visitor may still cancel or ask to move an
// appointment through its link: it is pending or confirmed and has not
// started.
func actionable(s Status, startsAt, now time.Time) bool {
	return (s == Pending || s == Confirmed) && startsAt.After(now)
}

// late reports a cancellation inside the notice period. It compares real
// hours, so across a daylight-saving change the wall-clock gap may differ.
func late(startsAt, now time.Time, notice time.Duration) bool {
	return startsAt.Sub(now) < notice
}

// lockByToken locks the appointment a management link names for a change,
// refusing an unknown or expired link and an appointment past changing.
func lockByToken(ctx context.Context, q db.Querier, token string, now time.Time) (db.Appointment, error) {
	appt, err := q.LockAppointmentByTokenHash(ctx, tokens.Hash(token))
	if errors.Is(err, pgx.ErrNoRows) || err == nil && linkExpired(Status(appt.Status), appt.EndsAt, now) {
		return db.Appointment{}, errLinkNotFound
	}
	if err != nil {
		return db.Appointment{}, err
	}
	if !actionable(Status(appt.Status), appt.StartsAt, now) {
		return db.Appointment{}, errLinkNotAllowed
	}
	return appt, nil
}
