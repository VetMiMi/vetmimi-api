package booking

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
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
// act on them, and returns the service.
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
	if err := checkService(svc, r.Format); err != nil {
		return db.Service{}, err
	}
	if !r.PrivacyAcknowledged || !r.PolicyAcknowledged {
		return db.Service{}, errAcknowledgement
	}
	if err := checkWindow(cur, r.StartsAt, now); err != nil {
		return db.Service{}, err
	}
	return svc, checkSlot(ctx, q, cur, slotRequest{Service: svc, Start: r.StartsAt}, now)
}

// checkService requires svc to take bookings or requests in format.
func checkService(svc db.Service, format string) error {
	if !bookable(svc) {
		return errNotBookable
	}
	if !slices.Contains(svc.Formats, format) {
		e := unprocessable("/format", "is not offered for this service")
		return &e
	}
	return nil
}

// checkWindow requires start to fall after the minimum notice and before
// the end of the furthest bookable day.
func checkWindow(cur settings.Settings, start, now time.Time) error {
	loc, err := cur.Location()
	if err != nil {
		return err
	}
	today := now.In(loc)
	horizon := time.Date(today.Year(), today.Month(), today.Day()+cur.MaxAdvanceDays+1, 0, 0, 0, 0, loc)
	if start.Before(now.Add(time.Duration(cur.MinNoticeHours)*time.Hour)) || !start.Before(horizon) {
		return errOutsideWindow
	}
	return nil
}

// slotRequest is a start to check against the free slots.
type slotRequest struct {
	Service db.Service
	// Duration is the session's length; zero means the service's.
	Duration time.Duration
	Start    time.Time
	// Moving is an appointment being rescheduled. Its own time counts as
	// free to it, and Daw Mi may move it inside the minimum notice or past
	// the furthest bookable day; never onto other busy time.
	Moving pgtype.UUID
}

// anyDay stands in for the furthest bookable day when Daw Mi moves an
// appointment.
const anyDay = 100 * 365

// checkSlot requires r's start to be one of the free slots now, computed
// exactly as public availability computes them, and otherwise offers up to
// five free slots from the same day on. The exclusion constraint stays the
// guard against a race this check misses.
func checkSlot(ctx context.Context, q db.Querier, cur settings.Settings, r slotRequest, now time.Time) error {
	loc, err := cur.Location()
	if err != nil {
		return err
	}
	duration := r.Duration
	if duration == 0 {
		duration = time.Duration(r.Service.DurationMinutes.Int32) * time.Minute
	}
	local := r.Start.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	in, err := slotInput(ctx, q, r.Service, duration, cur, day, day.AddDate(0, 0, alternativeDays-1), now, r.Moving)
	if err != nil {
		return err
	}
	if r.Moving.Valid {
		in.MinNotice, in.MaxAdvanceDays = 0, anyDay
	}
	var alternatives []Period
	for _, d := range FreeSlots(in) {
		for _, slot := range d.Slots {
			if slot.Start.Equal(r.Start) && sameDate(d.Date, day) {
				return nil
			}
			if len(alternatives) < maxAlternatives {
				alternatives = append(alternatives, slot)
			}
		}
	}
	return slotUnavailable(alternatives)
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
