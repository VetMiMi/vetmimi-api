package booking

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// maxAlternatives and alternativeDays bound the free slots offered when the
// requested one is gone: up to five, on the same or the next two weeks' days.
const (
	maxAlternatives = 5
	alternativeDays = 14
)

var (
	errOutsideWindow = apperr.New(apperr.OutsideBookingWindow,
		"The start is inside the minimum notice or beyond the furthest bookable day.")
	errAcknowledgement = apperr.New(apperr.AcknowledgementRequired,
		"The privacy notice and the booking policy must both be acknowledged.")
)

// checkRequest applies the booking rules to r in the order the visitor can
// act on them, and returns the service. The start must be one of the free
// slots now, computed exactly as public availability computes them; the
// exclusion constraint stays the guard against a race this check misses.
func checkRequest(ctx context.Context, q db.Querier, cur settings.Settings, r Request, now time.Time) (db.Service, error) {
	if !cur.PublicBookingEnabled {
		return db.Service{}, errBookingPaused
	}
	svc, err := q.GetServiceBySlug(ctx, r.Service)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errNotBookable
	}
	if err != nil {
		return db.Service{}, err
	}
	if !bookable(svc) {
		return db.Service{}, errNotBookable
	}
	if !slices.Contains(svc.Formats, r.Format) {
		e := unprocessable("/format", "is not offered for this service")
		return db.Service{}, &e
	}
	if !r.PrivacyAcknowledged || !r.PolicyAcknowledged {
		return db.Service{}, errAcknowledgement
	}

	loc, err := cur.Location()
	if err != nil {
		return db.Service{}, err
	}
	today := now.In(loc)
	horizon := time.Date(today.Year(), today.Month(), today.Day()+cur.MaxAdvanceDays+1, 0, 0, 0, 0, loc)
	if r.StartsAt.Before(now.Add(time.Duration(cur.MinNoticeHours)*time.Hour)) || !r.StartsAt.Before(horizon) {
		return db.Service{}, errOutsideWindow
	}

	local := r.StartsAt.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	free, err := slots(ctx, q, svc, cur, day, day.AddDate(0, 0, alternativeDays-1), now)
	if err != nil {
		return db.Service{}, err
	}
	var alternatives []Period
	for _, d := range free.Days {
		for _, slot := range d.Slots {
			if slot.Start.Equal(r.StartsAt) && sameDate(d.Date, day) {
				return svc, nil
			}
			if len(alternatives) < maxAlternatives {
				alternatives = append(alternatives, slot)
			}
		}
	}
	return db.Service{}, slotUnavailable(alternatives)
}

// slotJSON is a free slot as the alternatives in a slot_unavailable problem
// show it (openapi.yaml, Slot).
type slotJSON struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

func slotUnavailable(alternatives []Period) error {
	out := make([]slotJSON, len(alternatives))
	for i, p := range alternatives {
		out[i] = slotJSON{StartsAt: p.Start.UTC(), EndsAt: p.End.UTC()}
	}
	return &apperr.Error{Code: apperr.SlotUnavailable, Detail: "The requested time is no longer free.",
		Extensions: map[string]any{"alternatives": out}}
}

// withAlternatives gives a slot_unavailable from the exclusion constraint
// the alternatives member the contract requires. The transaction has failed,
// so none can be read; the schedule lock makes this path a backstop only.
func withAlternatives(err error) error {
	var e *apperr.Error
	if errors.As(err, &e) && e.Code == apperr.SlotUnavailable {
		return slotUnavailable(nil)
	}
	return err
}
