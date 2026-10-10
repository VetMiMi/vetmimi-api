package booking_test

import (
	"context"
	"expvar"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func counter(name, key string) int64 {
	v := expvar.Get(name).(*expvar.Map).Get(key)
	if v == nil {
		return 0
	}
	return v.(*expvar.Int).Value()
}

func reschedule(t *testing.T, c booking.Change, start, now time.Time) (booking.Changed, error) {
	t.Helper()
	return booking.Reschedule(context.Background(), pgtest.Pool(t), c, start, now)
}

func TestReschedule_MovesConfirmedWithItsReminder(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Confirmed)
	to := appt.StartsAt.Add(4 * time.Hour)
	changed, err := reschedule(t, changeOf(t, appt), to, now)
	require.NoError(t, err)

	got := getAppointment(t, appt.ID)
	require.Equal(t, "confirmed", got.Status, "the status stays")
	require.True(t, to.Equal(got.StartsAt))
	require.True(t, to.Add(time.Hour).Equal(got.EndsAt))
	require.EqualValues(t, 60, got.DurationMinutes)
	require.Equal(t, appt.Version+1, got.Version)

	var prevStart, newStart time.Time
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT lower(previous_range), lower(new_range) FROM appointment_events
		 WHERE appointment_id = $1 AND kind = 'rescheduled' AND actor = 'admin'`, appt.ID).Scan(&prevStart, &newStart))
	require.True(t, appt.StartsAt.Equal(prevStart))
	require.True(t, to.Equal(newStart))

	reminders := remindersOf(t, appt.ID)
	require.Len(t, reminders, 2)
	require.Equal(t, "cancelled", reminders[0].status)
	require.Equal(t, "queued", reminders[1].status)
	require.True(t, to.Add(-24*time.Hour).Equal(reminders[1].scheduledFor))
	require.Contains(t, commStatuses(t, appt.ID), "rescheduled:queued:")
	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver}, taskTypes(changed.Tasks))

	moved := getAppointment(t, appt.ID)
	_, err = reschedule(t, changeOf(t, moved), to.Add(30*time.Minute), now)
	require.NoError(t, err, "its own buffer does not stand in its way")
}

func TestReschedule_PendingKeepsItsHold(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Pending)
	c := changeOf(t, appt)
	c.Notify = false
	changed, err := reschedule(t, c, appt.StartsAt.Add(5*time.Hour), now)
	require.NoError(t, err)
	got := getAppointment(t, appt.ID)
	require.Equal(t, "pending", got.Status)
	require.True(t, appt.HoldExpiresAt.Time.Equal(got.HoldExpiresAt.Time), "the hold never outlasts the start")
	require.Equal(t, "hold:"+appt.ID.String(), changed.Replace[0].ID)
	require.Empty(t, commStatuses(t, appt.ID), "no email when Daw Mi tells them herself")
}

func TestReschedule_FailureLeavesOriginal(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Confirmed)
	other, err := insert(t, appointment(t, booking.Confirmed, appt.StartsAt.Add(4*time.Hour)))
	require.NoError(t, err)
	before := rowJSON(t, appt.ID)

	tests := map[string]struct {
		change func(*booking.Change)
		start  time.Time
		code   apperr.Code
	}{
		"onto another appointment": {nil, other.StartsAt, apperr.SlotUnavailable},
		"into its buffer":          {nil, other.StartsAt.Add(-time.Hour), apperr.SlotUnavailable},
		"outside opening hours":    {nil, appt.StartsAt.Add(10 * time.Hour), apperr.SlotUnavailable},
		"into the past":            {nil, now.Add(-time.Hour), apperr.OutsideBookingWindow},
		"stale version":            {func(c *booking.Change) { c.Version++ }, appt.StartsAt.Add(2 * time.Hour), apperr.StaleVersion},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := changeOf(t, appt)
			if tc.change != nil {
				tc.change(&c)
			}
			_, err := reschedule(t, c, tc.start, now)
			requireCode(t, tc.code, err)
			require.Equal(t, before, rowJSON(t, appt.ID), "the original is untouched")
		})
	}
}

// The minimum notice binds visitors, not Daw Mi.
func TestReschedule_InsideNoticeAllowedForDawMi(t *testing.T) {
	openEveryDay(t)
	appt, _ := booked(t, booking.Confirmed)
	now := appt.StartsAt.Add(-3 * time.Hour)
	to := appt.StartsAt.Add(4 * time.Hour)
	_, err := reschedule(t, changeOf(t, appt), to, now)
	require.NoError(t, err)
	require.True(t, to.Equal(getAppointment(t, appt.ID).StartsAt))
}

func TestReschedule_FinalRefused(t *testing.T) {
	appt, now := booked(t, booking.Confirmed)
	_, err := booking.Cancel(context.Background(), pgtest.Pool(t), changeOf(t, appt), now)
	require.NoError(t, err)
	_, err = reschedule(t, changeOf(t, getAppointment(t, appt.ID)), appt.StartsAt.Add(time.Hour), now)
	requireCode(t, apperr.InvalidTransition, err)
}

// ADR-004: two appointments moved into the same free time at once; one
// moves, the other keeps its time.
func TestReschedule_TwoIntoOneSlot(t *testing.T) {
	openEveryDay(t)
	first, now := booked(t, booking.Confirmed)
	second, err := insert(t, appointment(t, booking.Confirmed, first.StartsAt.Add(2*time.Hour)))
	require.NoError(t, err)
	target := first.StartsAt.Add(5 * time.Hour)
	conflicts := counter("slot_conflicts", "reschedule")

	errs := make(chan error, 2)
	for _, appt := range []booking.Change{changeOf(t, first), changeOf(t, second)} {
		go func() {
			_, err := booking.Reschedule(context.Background(), pgtest.Pool(t), appt, target, now)
			errs <- err
		}()
	}
	a, b := <-errs, <-errs
	if a != nil {
		a, b = b, a
	}
	require.NoError(t, a)
	requireCode(t, apperr.SlotUnavailable, b)
	require.Equal(t, conflicts+1, counter("slot_conflicts", "reschedule"))

	firstNow, secondNow := getAppointment(t, first.ID).StartsAt, getAppointment(t, second.ID).StartsAt
	moved := firstNow.Equal(target) && secondNow.Equal(second.StartsAt) ||
		secondNow.Equal(target) && firstNow.Equal(first.StartsAt)
	require.True(t, moved, "one moved and the loser keeps its time")
}
