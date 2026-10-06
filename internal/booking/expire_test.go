package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestExpireHold_ExpiresAndTellsTheVisitor(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	got, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)
	hold := now.Add(48 * time.Hour)
	ctx := context.Background()
	pool := pgtest.Pool(t)

	tasks, err := booking.ExpireHold(ctx, pool, got.AppointmentID, hold.Add(-time.Second))
	require.NoError(t, err)
	require.Empty(t, tasks, "a task that fires early changes nothing")
	require.Equal(t, "pending", getAppointment(t, got.AppointmentID).Status)

	tasks, err = booking.ExpireHold(ctx, pool, got.AppointmentID, hold)
	require.NoError(t, err)
	require.Equal(t, []string{comms.TaskDeliver}, taskTypes(tasks))
	appt := getAppointment(t, got.AppointmentID)
	require.Equal(t, "expired", appt.Status)
	require.Equal(t, int32(2), appt.Version)
	require.True(t, hold.Equal(appt.HoldExpiresAt.Time), "the lapsed hold stays on the row")
	require.Contains(t, commKinds(t, appt.ID), "request_expired:visitor@example.com:my")
	require.Equal(t, 1, countRows(t, "SELECT count(*) FROM appointment_events WHERE appointment_id = $1 AND kind = 'expired' AND actor = 'system'", appt.ID))

	tasks, err = booking.ExpireHold(ctx, pool, got.AppointmentID, hold.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, tasks, "expiring twice sends nothing more")
	require.Equal(t, 1, countRows(t, "SELECT count(*) FROM communications WHERE appointment_id = $1 AND kind = 'request_expired'", appt.ID))

	// The slot is free again for the next visitor, still just inside the notice.
	_, err = request(t, visitorRequest(newKey(t), "individual-art-therapy", start), hold)
	require.NoError(t, err)
}

func TestExpireHold_ConfirmedUntouched(t *testing.T) {
	appt, err := insert(t, appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour)))
	require.NoError(t, err)
	before := rowJSON(t, appt.ID)

	tasks, err := booking.ExpireHold(context.Background(), pgtest.Pool(t), appt.ID, appt.StartsAt)
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.Equal(t, before, rowJSON(t, appt.ID))
}

func TestSweepHolds_ExpiresOnlyOverdue(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	var ids [3]booking.Requested
	for i := range ids {
		r, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start.Add(time.Duration(2*i)*time.Hour)),
			now.Add(time.Duration(i)*time.Hour))
		require.NoError(t, err)
		ids[i] = r
	}

	// Holds end at now+48h, +49h and +50h; the sweep runs at +49h.
	_, err := booking.SweepHolds(context.Background(), pgtest.Pool(t), now.Add(49*time.Hour))
	require.NoError(t, err)
	require.Equal(t, "expired", getAppointment(t, ids[0].AppointmentID).Status)
	require.Equal(t, "expired", getAppointment(t, ids[1].AppointmentID).Status)
	require.Equal(t, "pending", getAppointment(t, ids[2].AppointmentID).Status)
}

func TestExpireHold_HoldHoursChangeAffectsNewRequestsOnly(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	first, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)
	setSetting(t, "pending_hold_hours", "2")
	second, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start.Add(2*time.Hour)), now)
	require.NoError(t, err)

	require.True(t, now.Add(48*time.Hour).Equal(getAppointment(t, first.AppointmentID).HoldExpiresAt.Time))
	require.True(t, now.Add(2*time.Hour).Equal(getAppointment(t, second.AppointmentID).HoldExpiresAt.Time))
}
