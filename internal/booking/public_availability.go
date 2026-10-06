package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

var errBookingPaused = apperr.New(apperr.BookingPaused, "Public booking is paused.")

// PublicAvailability is the free slots for the service with slug on local
// dates first to last, while public booking is open.
func PublicAvailability(ctx context.Context, q db.Querier, slug string, first, last, now time.Time) (Availability, error) {
	svc, err := q.GetServiceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Availability{}, apperr.New(apperr.NotFound, "No service has this slug.")
	}
	if err != nil {
		return Availability{}, err
	}
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	if !cur.PublicBookingEnabled {
		return Availability{}, errBookingPaused
	}
	return slots(ctx, q, svc, cur, first, last, now)
}
