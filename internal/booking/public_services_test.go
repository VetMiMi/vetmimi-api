package booking_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// setBookingEnabled stores public_booking_enabled and restores it after t.
func setBookingEnabled(t *testing.T, enabled bool) {
	t.Helper()
	set := func(v bool) {
		_, err := pgtest.Pool(t).Exec(context.Background(),
			"UPDATE settings SET value = to_jsonb($1::boolean) WHERE key = 'public_booking_enabled'", v)
		require.NoError(t, err)
	}
	set(enabled)
	t.Cleanup(func() { set(true) })
}

func TestPublicServices_HidesPaused(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	// Seeded: individual-art-therapy (request), free-consultation (paused)
	// and two enquiry-only services.
	svc, err := booking.CreateService(ctx, q, db.CreateServiceParams{
		Slug: "studio-session", Name: []byte(`{"en": "Studio session", "my": "စတူဒီယို"}`),
		Description: []byte(`{"en": "Two hours in the studio"}`), FeeText: []byte(`{"en": "$120", "my": "၁၂၀"}`),
		BookingAction: "book", DurationMinutes: pgtype.Int4{Int32: 120, Valid: true}, BufferBeforeMinutes: 10, SortOrder: 9,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, booking.DeleteService(ctx, q, svc.ID)) })

	list, err := booking.ListPublicServices(ctx, q, "my")
	require.NoError(t, err)
	require.True(t, list.BookingEnabled)
	require.Equal(t, "request_approval", list.BookingMode)
	require.Equal(t, "Australia/Sydney", list.Timezone)
	require.Len(t, list.Items, 2)
	require.Equal(t, "individual-art-therapy", list.Items[0].Slug)
	require.Equal(t, booking.PublicService{
		Slug: "studio-session", Name: "စတူဒီယို", Description: "Two hours in the studio", BookingAction: "book",
		DurationMinutes: 120, Formats: []string{"online"}, FeeText: "၁၂၀",
	}, list.Items[1], "Burmese where it is written, English where it is not")
}

func TestPublicServices_EmptyWhileBookingPaused(t *testing.T) {
	setBookingEnabled(t, false)
	list, err := booking.ListPublicServices(context.Background(), db.New(pgtest.Pool(t)), "en")
	require.NoError(t, err)
	require.False(t, list.BookingEnabled)
	require.Empty(t, list.Items)

	therapy := therapy(t)
	require.Equal(t, "active", therapy.State, "pausing booking leaves services alone")
}
