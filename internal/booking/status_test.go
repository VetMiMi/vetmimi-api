package booking_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
)

func TestStatus_PendingToConfirmed(t *testing.T) {
	require.True(t, booking.CanTransition(booking.Pending, booking.Confirmed))
	require.True(t, booking.CanTransition(booking.Confirmed, booking.Completed))
	require.True(t, booking.CanTransition(booking.Pending, booking.CancelledByClient))
}

// Pending is a request, not a booking: it never completes or no-shows
// without being confirmed first.
func TestStatus_PendingIsNotConfirmed(t *testing.T) {
	require.False(t, booking.CanTransition(booking.Pending, booking.Completed))
	require.False(t, booking.CanTransition(booking.Pending, booking.NoShow))
	require.False(t, booking.CanTransition(booking.Pending, booking.CancelledByPractitioner))
}

func TestStatus_FinalStatusesGoNowhere(t *testing.T) {
	require.False(t, booking.CanTransition(booking.Completed, booking.Confirmed))
	for _, s := range []booking.Status{booking.Declined, booking.Expired, booking.CancelledByClient,
		booking.CancelledByPractitioner, booking.Completed, booking.NoShow} {
		require.True(t, booking.Terminal(s), s)
	}
	require.False(t, booking.Terminal(booking.Pending))
	require.False(t, booking.Terminal(booking.Confirmed))
}
