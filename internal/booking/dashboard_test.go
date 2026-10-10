package booking_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// dashboardNow is 10:00 on Sunday 5 April 2026 in Sydney, the day daylight saving ends, so the local day is
// 25 hours long.
var dashboardNow = time.Date(2026, 4, 5, 10, 0, 0, 0, sydney)

// dashboardFixture runs inside a transaction that starts from no appointments or blocks and is rolled back,
// since the dashboard reads the whole table and the package shares one database.
type dashboardFixture struct {
	t     *testing.T
	tx    pgx.Tx
	q     *db.Queries
	names map[pgtype.UUID]string
}

func newDashboardFixture(t *testing.T) *dashboardFixture {
	t.Helper()
	ctx := context.Background()
	tx, err := pgtest.Pool(t).Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	for _, table := range []string{"appointments", "availability_blocks"} {
		_, err := tx.Exec(ctx, "DELETE FROM "+table)
		require.NoError(t, err)
	}
	return &dashboardFixture{t: t, tx: tx, q: db.New(tx), names: map[pgtype.UUID]string{}}
}

func (f *dashboardFixture) add(name string, status booking.Status, startsAt time.Time) db.Appointment {
	f.t.Helper()
	a := appointment(f.t, status, startsAt)
	if status == booking.Pending {
		a.HoldExpiresAt = sql.NullTime{Time: dashboardNow.Add(48 * time.Hour), Valid: true}
	}
	return f.insert(name, a)
}

func (f *dashboardFixture) insert(name string, a booking.NewAppointment) db.Appointment {
	f.t.Helper()
	appt, err := booking.InsertAppointment(context.Background(), f.q, testSecret, a)
	require.NoError(f.t, err)
	f.names[appt.ID] = name
	return appt
}

func (f *dashboardFixture) message(appt db.Appointment, status string, at time.Time) {
	f.t.Helper()
	_, err := f.tx.Exec(context.Background(), `INSERT INTO communications
		(appointment_id, kind, audience, recipient, locale, status, sent_at, created_at)
		VALUES ($1, 'booking_confirmed', 'visitor', 'visitor@example.com', 'en', $2,
		        CASE WHEN $2 = 'sent' THEN $3::timestamptz END, $3)`, appt.ID, status, at)
	require.NoError(f.t, err)
}

func (f *dashboardFixture) block(start, end time.Time) {
	f.t.Helper()
	_, err := f.tx.Exec(context.Background(), "INSERT INTO availability_blocks (period) VALUES (tstzrange($1, $2))",
		start, end)
	require.NoError(f.t, err)
}

func (f *dashboardFixture) dashboard() booking.DashboardData {
	f.t.Helper()
	d, err := booking.Dashboard(context.Background(), f.q, dashboardNow)
	require.NoError(f.t, err)
	return d
}

func (f *dashboardFixture) listNames(rows []db.ListAppointmentsRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = f.names[r.ID]
	}
	return out
}

func april(day, hour, minute int) time.Time {
	return time.Date(2026, 4, day, hour, minute, 0, 0, sydney)
}

func TestDashboard_AttentionOrderCountsAndLists(t *testing.T) {
	f := newDashboardFixture(t)
	ctx := context.Background()

	soon := appointment(t, booking.Pending, april(5, 23, 30)) // the last half hour of the 25-hour day
	soon.HoldExpiresAt = sql.NullTime{Time: dashboardNow.Add(3 * time.Hour), Valid: true}
	f.insert("pendingSoon", soon)
	f.add("pendingLater", booking.Pending, april(8, 10, 0))
	failed := f.add("failedEmail", booking.Confirmed, april(6, 15, 0))
	f.message(failed, "failed", dashboardNow.Add(-time.Hour))
	resent := f.add("resentEmail", booking.Confirmed, april(7, 15, 0))
	f.message(resent, "failed", dashboardNow.Add(-2*time.Hour))
	f.message(resent, "sent", dashboardNow.Add(-time.Hour))
	moving := f.add("rescheduleAsked", booking.Confirmed, april(9, 15, 0))
	require.NoError(t, booking.AppendEvent(ctx, f.q, booking.Event{AppointmentID: moving.ID,
		Kind: "reschedule_requested", Actor: "visitor"}))
	f.add("dueThursday", booking.Confirmed, april(2, 10, 0))
	f.add("dueThisMorning", booking.Confirmed, april(5, 8, 0))
	f.add("blocked", booking.Confirmed, april(10, 15, 0))
	f.add("cancelledBlocked", booking.CancelledByPractitioner, april(10, 15, 0))
	f.block(april(10, 14, 0), april(10, 16, 0))
	f.add("monday", booking.Confirmed, april(6, 1, 0))

	d := f.dashboard()
	require.Equal(t, "Australia/Sydney", d.Timezone)

	type item struct{ kind, name, detail string }
	var got []item
	for _, a := range d.Attention {
		got = append(got, item{a.Kind, f.names[a.AppointmentID], a.Detail})
	}
	require.Equal(t, []item{
		{"pending_request", "pendingSoon", "Request waiting for a decision"},
		{"pending_request", "pendingLater", "Request waiting for a decision"},
		{"hold_expiring", "pendingSoon", "Hold ends in 3 h"},
		{"failed_communication", "failedEmail", "Email failed: booking confirmed"},
		{"reschedule_requested", "rescheduleAsked", "Visitor asked to reschedule"},
		{"completion_due", "dueThursday", "Mark as completed or no-show"},
		{"completion_due", "dueThisMorning", "Mark as completed or no-show"},
		{"block_conflict", "blocked", "Overlaps a blocked time"},
	}, got, "a resent email and a cancelled appointment under a block need nothing")

	require.Equal(t, db.DashboardCountsRow{Pending: 2, Confirmed: 5, Today: 2, ThisWeek: 3}, d.Counts)
	require.Equal(t, []string{"pendingSoon", "pendingLater"}, f.listNames(d.Pending))
	require.Equal(t, []string{"dueThisMorning", "pendingSoon"}, f.listNames(d.Today),
		"23:30 is still today on a 25-hour day; 01:00 Monday is not")
	require.Equal(t, []string{"monday", "failedEmail", "resentEmail", "pendingLater", "rescheduleAsked", "blocked"},
		f.listNames(d.Upcoming))
}

func TestDashboard_RescheduleRequestClearedByReschedule(t *testing.T) {
	f := newDashboardFixture(t)
	ctx := context.Background()
	appt := f.add("moved", booking.Confirmed, april(9, 15, 0))
	require.NoError(t, booking.AppendEvent(ctx, f.q, booking.Event{AppointmentID: appt.ID,
		Kind: "reschedule_requested", Actor: "visitor"}))
	require.NoError(t, booking.AppendEvent(ctx, f.q, booking.Event{AppointmentID: appt.ID,
		Kind: "rescheduled", Actor: "admin"}))

	require.Empty(t, f.dashboard().Attention)
}
