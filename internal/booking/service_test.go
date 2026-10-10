package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// The database's services_duration_required decides, not a Go check.
func TestCreateService_RequestNeedsDuration(t *testing.T) {
	_, err := booking.CreateService(context.Background(), db.New(pgtest.Pool(t)), db.CreateServiceParams{
		Slug: "no-duration", Name: []byte(`{"en": "No duration"}`), BookingAction: "request",
	})
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/durationMinutes", e.Fields[0].Field)
}

func TestResumeService(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	free, err := q.GetServiceBySlug(ctx, "free-consultation")
	require.NoError(t, err)
	require.Equal(t, "paused", free.State)

	resumed, err := booking.SetServiceState(ctx, q, free.ID, free.Version, time.Now(), "active")
	require.NoError(t, err)
	require.Equal(t, "active", resumed.State)
	require.Equal(t, free.Version+1, resumed.Version)
	t.Cleanup(func() {
		_, err := booking.SetServiceState(ctx, q, free.ID, resumed.Version, time.Now(), "paused")
		require.NoError(t, err)
	})

	_, err = booking.SetServiceState(ctx, q, free.ID, resumed.Version, time.Now(), "active")
	requireCode(t, apperr.InvalidTransition, err)
	_, err = booking.SetServiceState(ctx, q, free.ID, free.Version, time.Now(), "paused")
	requireCode(t, apperr.StaleVersion, err)
}

// New durations apply to future bookings only: an appointment keeps the
// busy range it was booked with.
func TestUpdateServiceLeavesAppointmentsAlone(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	svc, err := booking.CreateService(ctx, q, db.CreateServiceParams{
		Slug: "longer-later", Name: []byte(`{"en": "Longer later"}`), BookingAction: "request",
		DurationMinutes: pgtype.Int4{Int32: 50, Valid: true}, BufferAfterMinutes: 10,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM appointments WHERE service_id = $1", svc.ID)
		require.NoError(t, err)
		require.NoError(t, booking.DeleteService(ctx, q, svc.ID))
	})
	a := appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour))
	a.Service = svc
	booked, err := insert(t, a)
	require.NoError(t, err)
	before := rowJSON(t, booked.ID)

	_, err = booking.UpdateService(ctx, q, svc.ID, svc.Version, time.Now(), func(p *db.UpdateServiceParams) {
		p.DurationMinutes = pgtype.Int4{Int32: 90, Valid: true}
		p.BufferAfterMinutes = 30
	})
	require.NoError(t, err)

	require.Equal(t, before, rowJSON(t, booked.ID))

	requireCode(t, apperr.InUse, booking.DeleteService(ctx, q, svc.ID))
}
