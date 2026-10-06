package booking_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// sundayInput is the therapy fixture on one Sunday with the given hours.
func sundayInput(day time.Time, start, end string, now time.Time) booking.SlotInput {
	in := therapyInput()
	in.First, in.Last, in.Now = day, day, now
	in.Rules = []db.AvailabilityRule{weekly(sunday, start, end)}
	return in
}

// On Sunday 5 April 2026 Sydney's clocks go back from 03:00 AEDT to 02:00
// AEST, so 02:00-02:59 happens twice. Each wall-clock time is offered once,
// at its first instant.
func TestFreeSlots_AprilRepeatOfferedOnce(t *testing.T) {
	in := sundayInput(date(2026, 4, 5), "01:00", "04:00", at(2026, 3, 1, 9, 0))
	require.Equal(t, []string{"2026-04-05 01:00 +11", "2026-04-05 01:30 +11", "2026-04-05 02:00 +11", "2026-04-05 02:30 +11"},
		starts(t, in))
}

// On Sunday 4 October 2026 the clocks go forward from 02:00 AEST to 03:00
// AEDT, so no 02:xx time exists and none is offered.
func TestFreeSlots_OctoberGapDropped(t *testing.T) {
	in := sundayInput(date(2026, 10, 4), "01:00", "05:00", at(2026, 9, 1, 9, 0))
	require.Equal(t, []string{"2026-10-04 01:00 +10", "2026-10-04 01:30 +10", "2026-10-04 03:00 +11", "2026-10-04 03:30 +11"},
		starts(t, in))
}

// Minimum notice is real hours: across either change, 24 hours is not "the
// same time tomorrow".
func TestFreeSlots_NoticeIsRealHoursAcrossDST(t *testing.T) {
	april := sundayInput(date(2026, 4, 5), "01:00", "06:00", at(2026, 4, 4, 3, 0))
	april.MinNotice = 24 * time.Hour
	require.Equal(t, []string{"2026-04-05 02:00 +10", "2026-04-05 02:30 +10", "2026-04-05 03:00 +10",
		"2026-04-05 03:30 +10", "2026-04-05 04:00 +10", "2026-04-05 04:30 +10"}, starts(t, april))

	october := sundayInput(date(2026, 10, 4), "01:00", "06:00", at(2026, 10, 3, 3, 0))
	october.MinNotice = 24 * time.Hour
	require.Equal(t, []string{"2026-10-04 04:00 +11", "2026-10-04 04:30 +11"}, starts(t, october))
}
