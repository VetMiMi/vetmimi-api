package booking_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func linkOf(appt db.Appointment) string {
	return platform.NewManagementToken(testSecret, appt.ManagementTokenSeed)
}

func getManaged(t *testing.T, token string, now time.Time) (booking.Managed, error) {
	t.Helper()
	return booking.GetManaged(context.Background(), db.New(pgtest.Pool(t)), token, now)
}

func TestManage_ShowsTheAppointmentWithoutTheVisitor(t *testing.T) {
	appt, now := booked(t, booking.Confirmed)
	m, err := getManaged(t, linkOf(appt), now)
	require.NoError(t, err)
	require.Equal(t, booking.Managed{
		Reference: appt.Reference, Status: booking.Confirmed,
		ServiceSlug: "individual-art-therapy", ServiceName: "Individual Art Therapy",
		StartsAt: appt.StartsAt.UTC(), EndsAt: appt.EndsAt.UTC(), DurationMinutes: 60,
		Timezone: "Australia/Sydney", Format: "online",
		CanCancel: true, CanRequestReschedule: true, CancellationNoticeHours: 48,
	}, m, "72 hours ahead is outside the notice period")

	m, err = getManaged(t, linkOf(appt), appt.StartsAt.Add(-12*time.Hour))
	require.NoError(t, err)
	require.True(t, m.LateIfCancelledNow)
}

// An unknown link and one for an appointment that is over look the same.
func TestManage_UnknownTokenNeutral404(t *testing.T) {
	appt, now := booked(t, booking.Confirmed)
	_, err := getManaged(t, strings.Repeat("A", 43), now)
	unknown := requireCode(t, apperr.NotFound, err)

	_, err = booking.Complete(context.Background(), pgtest.Pool(t), changeOf(t, appt), appt.EndsAt)
	require.NoError(t, err)
	_, err = getManaged(t, linkOf(appt), appt.EndsAt.Add(time.Minute))
	over := requireCode(t, apperr.NotFound, err)
	require.Equal(t, unknown, over)

	_, err = getManaged(t, platform.NewManagementToken([]byte("another secret"), appt.ManagementTokenSeed), now)
	requireCode(t, apperr.NotFound, err)
}

func TestManageCancel_OutsideNoticeIsNotLate(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Confirmed)
	changed, err := booking.CancelByClient(context.Background(), pgtest.Pool(t), linkOf(appt), " See you later. ", now)
	require.NoError(t, err)
	require.Len(t, changed.Tasks, 2)

	got := getAppointment(t, appt.ID)
	require.Equal(t, "cancelled_by_client", got.Status)
	require.False(t, got.LateCancellation)
	require.Equal(t, []string{"cancelled", "confirmed", "cancelled_by_client", "visitor",
		`{"by": "client", "late_cancellation": false}`}, lastEvent(t, appt.ID))
	require.Equal(t, []string{"cancelled:queued:", "practitioner_client_cancelled:queued:See you later.",
		"reminder:cancelled:"}, commStatuses(t, appt.ID))

	_, err = request(t, visitorRequest(newKey(t), "individual-art-therapy", appt.StartsAt), now)
	require.NoError(t, err, "the time is free again")

	m, err := getManaged(t, linkOf(appt), now)
	require.NoError(t, err, "the link still shows a cancelled appointment until it is over")
	require.False(t, m.CanCancel)
}

func TestManageCancel_LateFlag(t *testing.T) {
	appt, _ := booked(t, booking.Pending)
	changed, err := booking.CancelByClient(context.Background(), pgtest.Pool(t), linkOf(appt), "",
		appt.StartsAt.Add(-12*time.Hour))
	require.NoError(t, err)
	require.True(t, getAppointment(t, appt.ID).LateCancellation)
	require.Equal(t, "hold:"+appt.ID.String(), changed.Remove[0].ID, "a pending request's hold task goes")
	require.Contains(t, lastEvent(t, appt.ID)[4], `"late_cancellation": true`)
}

// Clocks go back at 03:00 on Sunday 5 April 2026. 11:00 on Friday 3 April
// to 10:00 on the Sunday is 47 wall-clock hours but 48 real ones, so it is
// not late.
func TestManageCancel_NoticeAcrossDST(t *testing.T) {
	ctx := context.Background()
	start := time.Date(2026, 4, 5, 10, 0, 0, 0, sydney)
	appt, err := insert(t, appointment(t, booking.Confirmed, start))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM appointments WHERE id = $1", appt.ID)
		require.NoError(t, err)
	})
	_, err = booking.CancelByClient(ctx, pgtest.Pool(t), linkOf(appt), "", time.Date(2026, 4, 3, 11, 0, 0, 0, sydney))
	require.NoError(t, err)
	require.False(t, getAppointment(t, appt.ID).LateCancellation)
}

func TestManageCancel_Refusals(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)

	declined, now := booked(t, booking.Pending)
	_, err := booking.Decline(ctx, pool, changeOf(t, declined), now)
	require.NoError(t, err)
	_, err = booking.CancelByClient(ctx, pool, linkOf(declined), "", now)
	requireCode(t, apperr.ActionNotAllowed, err)

	started, _ := booked(t, booking.Confirmed)
	_, err = booking.CancelByClient(ctx, pool, linkOf(started), "", started.StartsAt)
	requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "confirmed", getAppointment(t, started.ID).Status)

	_, err = booking.CancelByClient(ctx, pool, strings.Repeat("B", 43), "", now)
	requireCode(t, apperr.NotFound, err)
}

func TestManageReschedule_RecordsTheRequestAndKeepsTheTime(t *testing.T) {
	ctx := context.Background()
	appt, now := booked(t, booking.Pending)
	preferred := []time.Time{appt.StartsAt.Add(24 * time.Hour), appt.StartsAt.Add(48 * time.Hour)}
	changed, err := booking.RequestReschedule(ctx, pgtest.Pool(t), linkOf(appt), preferred, "Mornings only", now)
	require.NoError(t, err)
	require.Equal(t, []string{comms.TaskDeliver}, taskTypes(changed.Tasks))

	got := getAppointment(t, appt.ID)
	require.Equal(t, "pending", got.Status)
	require.Equal(t, appt.StartsAt, got.StartsAt, "the time stays booked")
	event := lastEvent(t, appt.ID)
	require.Equal(t, []string{"reschedule_requested", "", "", "visitor"}, event[:4])
	require.Contains(t, event[4], preferred[0].UTC().Format(time.RFC3339))
	require.Contains(t, commStatuses(t, appt.ID), "practitioner_reschedule_requested:queued:Mornings only")

	m, err := getManaged(t, linkOf(appt), now)
	require.NoError(t, err)
	require.True(t, m.RescheduleRequested)

	// A later status change answers the request.
	_, err = booking.Confirm(ctx, pgtest.Pool(t), testSecret, changeOf(t, got), now)
	require.NoError(t, err)
	m, err = getManaged(t, linkOf(appt), now)
	require.NoError(t, err)
	require.False(t, m.RescheduleRequested)
}

func TestManageReschedule_PastRefused(t *testing.T) {
	appt, _ := booked(t, booking.Confirmed)
	_, err := booking.RequestReschedule(context.Background(), pgtest.Pool(t), linkOf(appt), nil, "",
		appt.StartsAt.Add(time.Minute))
	requireCode(t, apperr.ActionNotAllowed, err)
}
