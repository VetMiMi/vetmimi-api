package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// booked stores an appointment at 10:00 Sydney on a free day, and a
// confirmed one's reminder, and returns it with a time three days before.
func booked(t *testing.T, status booking.Status) (db.Appointment, time.Time) {
	t.Helper()
	start, now := slotStart()
	appt, err := insert(t, appointment(t, status, start))
	require.NoError(t, err)
	if status == booking.Confirmed {
		_, err := booking.ScheduleReminder(context.Background(), db.New(pgtest.Pool(t)), appt, 24, now)
		require.NoError(t, err)
	}
	return appt, now
}

func changeOf(t *testing.T, appt db.Appointment) booking.Change {
	return booking.Change{ID: appt.ID, Version: appt.Version, Actor: practitioner(t), Notify: true}
}

// lastEvent is the newest history entry as kind, from, to and detail.
func lastEvent(t *testing.T, id pgtype.UUID) []string {
	t.Helper()
	var kind, actor, detail string
	var from, to pgtype.Text
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		`SELECT kind, from_status, to_status, actor, detail::text FROM appointment_events
		 WHERE appointment_id = $1 ORDER BY id DESC LIMIT 1`, id).Scan(&kind, &from, &to, &actor, &detail))
	return []string{kind, from.String, to.String, actor, detail}
}

func commStatuses(t *testing.T, id pgtype.UUID) []string {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(),
		"SELECT kind || ':' || status || ':' || coalesce(message, '') FROM communications WHERE appointment_id = $1 ORDER BY kind, created_at", id)
	require.NoError(t, err)
	var out []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestConfirm_ConfirmsAndQueuesConfirmationAndReminder(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	changed, err := booking.Confirm(context.Background(), pgtest.Pool(t), testSecret, changeOf(t, appt), now)
	require.NoError(t, err)

	got := getAppointment(t, appt.ID)
	require.Equal(t, "confirmed", got.Status)
	require.False(t, got.HoldExpiresAt.Valid, "the hold is cleared")
	require.Equal(t, appt.Version+1, got.Version)
	require.Equal(t, []string{"confirmed", "pending", "confirmed", "admin", "{}"}, lastEvent(t, appt.ID))
	require.Equal(t, []string{"booking_confirmed:queued:", "reminder:queued:"}, commStatuses(t, appt.ID))
	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver, video.TaskCloseRoom}, taskTypes(changed.Tasks))
	require.Len(t, changed.Remove, 1)
	require.Equal(t, "hold:"+appt.ID.String(), changed.Remove[0].ID, "the hold's expiry task is removed")
}

func TestConfirm_Refusals(t *testing.T) {
	ctx := context.Background()
	pending, now := booked(t, booking.Pending)
	stale := changeOf(t, pending)
	stale.Version++
	_, err := booking.Confirm(ctx, pgtest.Pool(t), testSecret, stale, now)
	requireCode(t, apperr.StaleVersion, err)

	_, err = booking.Confirm(ctx, pgtest.Pool(t), testSecret, changeOf(t, pending), pending.StartsAt)
	requireCode(t, apperr.InvalidTransition, err)

	declined, now := booked(t, booking.Pending)
	_, err = booking.Decline(ctx, pgtest.Pool(t), changeOf(t, declined), now)
	require.NoError(t, err)
	_, err = booking.Confirm(ctx, pgtest.Pool(t), testSecret, changeOf(t, getAppointment(t, declined.ID)), now)
	requireCode(t, apperr.InvalidTransition, err)

	missing := changeOf(t, pending)
	missing.ID = practitioner(t)
	_, err = booking.Confirm(ctx, pgtest.Pool(t), testSecret, missing, now)
	requireCode(t, apperr.NotFound, err)
}

// ADR-004: two administrators confirm the same request at once; one wins and
// the other is told the row changed.
func TestConfirm_TwoAdminsAtOnce(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := booking.Confirm(context.Background(), pgtest.Pool(t), testSecret, changeOf(t, appt), now)
			errs <- err
		}()
	}
	first, second := <-errs, <-errs
	if first != nil {
		first, second = second, first
	}
	require.NoError(t, first)
	requireCode(t, apperr.StaleVersion, second)
}

func TestDecline_ReopensTheSlotAndSendsTheMessage(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Pending)
	c := changeOf(t, appt)
	c.ToVisitor = "I am away that week."
	changed, err := booking.Decline(context.Background(), pgtest.Pool(t), c, now)
	require.NoError(t, err)

	got := getAppointment(t, appt.ID)
	require.Equal(t, "declined", got.Status)
	require.False(t, got.HoldExpiresAt.Valid)
	require.Equal(t, []string{"declined", "pending", "declined", "admin", "{}"}, lastEvent(t, appt.ID))
	require.Equal(t, []string{"request_declined:queued:I am away that week."}, commStatuses(t, appt.ID))
	require.Equal(t, "hold:"+appt.ID.String(), changed.Remove[0].ID)

	_, err = request(t, visitorRequest(newKey(t), "individual-art-therapy", appt.StartsAt), now)
	require.NoError(t, err, "the declined time is free again")
}

func TestDecline_ConfirmedRefused(t *testing.T) {
	appt, now := booked(t, booking.Confirmed)
	_, err := booking.Decline(context.Background(), pgtest.Pool(t), changeOf(t, appt), now)
	requireCode(t, apperr.InvalidTransition, err)
}

func TestCancel_CancelsReminderAndTellsTheVisitor(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Confirmed)
	c := changeOf(t, appt)
	c.ToVisitor = "I am unwell."
	_, err := booking.Cancel(context.Background(), pgtest.Pool(t), c, now)
	require.NoError(t, err)

	got := getAppointment(t, appt.ID)
	require.Equal(t, "cancelled_by_practitioner", got.Status)
	require.False(t, got.LateCancellation)
	require.Equal(t, []string{"cancelled", "confirmed", "cancelled_by_practitioner", "admin", `{"by": "practitioner"}`},
		lastEvent(t, appt.ID))
	require.Equal(t, []string{"cancelled:queued:I am unwell.", "reminder:cancelled:"}, commStatuses(t, appt.ID))

	_, err = request(t, visitorRequest(newKey(t), "individual-art-therapy", appt.StartsAt), now)
	require.NoError(t, err, "the cancelled time is free again")
}

func TestCancel_WithoutNotice(t *testing.T) {
	appt, now := booked(t, booking.Confirmed)
	c := changeOf(t, appt)
	c.Notify = false
	_, err := booking.Cancel(context.Background(), pgtest.Pool(t), c, now)
	require.NoError(t, err)
	require.Equal(t, []string{"reminder:cancelled:"}, commStatuses(t, appt.ID), "no email when Daw Mi tells them herself")
}

func TestCancel_Refusals(t *testing.T) {
	ctx := context.Background()
	pending, now := booked(t, booking.Pending)
	_, err := booking.Cancel(ctx, pgtest.Pool(t), changeOf(t, pending), now)
	requireCode(t, apperr.InvalidTransition, err) // a request is declined, not cancelled

	confirmed, _ := booked(t, booking.Confirmed)
	stale := changeOf(t, confirmed)
	stale.Version = 9
	_, err = booking.Cancel(ctx, pgtest.Pool(t), stale, now)
	requireCode(t, apperr.StaleVersion, err)
}

func TestComplete_AndNoShowOnlyAfterStart(t *testing.T) {
	ctx := context.Background()
	appt, now := booked(t, booking.Confirmed)
	_, err := booking.Complete(ctx, pgtest.Pool(t), changeOf(t, appt), now)
	requireCode(t, apperr.InvalidTransition, err) // not before it starts

	started := appt.StartsAt.Add(time.Minute)
	_, err = booking.Complete(ctx, pgtest.Pool(t), changeOf(t, appt), started)
	require.NoError(t, err)
	require.Equal(t, "completed", getAppointment(t, appt.ID).Status)
	require.Equal(t, []string{"completed", "confirmed", "completed", "admin", "{}"}, lastEvent(t, appt.ID))
	require.Equal(t, []string{"reminder:cancelled:"}, commStatuses(t, appt.ID), "nothing is sent")

	_, err = booking.MarkNoShow(ctx, pgtest.Pool(t), changeOf(t, getAppointment(t, appt.ID)), started)
	requireCode(t, apperr.InvalidTransition, err) // completed is final

	other, _ := booked(t, booking.Confirmed)
	_, err = booking.MarkNoShow(ctx, pgtest.Pool(t), changeOf(t, other), other.EndsAt)
	require.NoError(t, err)
	require.Equal(t, "no_show", getAppointment(t, other.ID).Status)
}

func TestSetNote_RecordsLengthNotText(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	require.NoError(t, booking.SetNote(context.Background(), pgtest.Pool(t), changeOf(t, appt), "Prefers mornings.", now))

	got := getAppointment(t, appt.ID)
	require.Equal(t, "Prefers mornings.", got.AdminNote.String)
	require.Equal(t, appt.Version+1, got.Version)
	require.Equal(t, []string{"note_updated", "", "", "admin", `{"length": 17}`}, lastEvent(t, appt.ID))

	err := booking.SetNote(context.Background(), pgtest.Pool(t), changeOf(t, appt), "Again.", now)
	requireCode(t, apperr.StaleVersion, err)
}

func TestAllowedActions(t *testing.T) {
	now := time.Date(2031, 3, 1, 9, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	tests := []struct {
		status booking.Status
		start  time.Time
		want   []string
	}{
		{booking.Pending, later, []string{"confirm", "decline", "reschedule"}},
		{booking.Confirmed, later, []string{"reschedule", "cancel"}},
		{booking.Confirmed, earlier, []string{"complete", "no_show"}},
		{booking.Confirmed, now, []string{"complete", "no_show"}},
		{booking.Declined, later, nil},
	}
	for _, tc := range tests {
		want := append(tc.want, "set_note", "mark_communicated")
		require.Equal(t, want, booking.AllowedActions(tc.status, tc.start, now), "%s at %s", tc.status, tc.start)
	}
}
