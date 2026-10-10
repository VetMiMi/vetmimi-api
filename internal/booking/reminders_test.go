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
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

type reminderRow struct {
	status       string
	scheduledFor time.Time
}

func remindersOf(t *testing.T, id pgtype.UUID) []reminderRow {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(),
		"SELECT status, scheduled_for FROM communications WHERE appointment_id = $1 AND kind = 'reminder' ORDER BY created_at", id)
	require.NoError(t, err)
	var out []reminderRow
	for rows.Next() {
		var r reminderRow
		require.NoError(t, rows.Scan(&r.status, &r.scheduledFor))
		out = append(out, r)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestScheduleReminder_OneRowAtTheOffset(t *testing.T) {
	appt, err := insert(t, appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour)))
	require.NoError(t, err)

	tasks, err := booking.ScheduleReminder(context.Background(), db.New(pgtest.Pool(t)), appt, 24,
		appt.StartsAt.Add(-72*time.Hour))
	require.NoError(t, err)

	want := appt.StartsAt.Add(-24 * time.Hour)
	require.Len(t, tasks, 1)
	require.Equal(t, comms.TaskDeliver, tasks[0].Type)
	require.True(t, want.Equal(tasks[0].ProcessAt))
	got := remindersOf(t, appt.ID)
	require.Len(t, got, 1)
	require.Equal(t, "queued", got[0].status)
	require.True(t, want.Equal(got[0].scheduledFor))
}

func TestReminder_NotCreatedInsideOffset(t *testing.T) {
	appt, err := insert(t, appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour)))
	require.NoError(t, err)

	tasks, err := booking.ScheduleReminder(context.Background(), db.New(pgtest.Pool(t)), appt, 24,
		appt.StartsAt.Add(-2*time.Hour))
	require.NoError(t, err)
	require.Empty(t, tasks)
	require.Empty(t, remindersOf(t, appt.ID))
}

// Reminder hours are real hours: across a daylight-saving change the
// reminder's wall-clock time differs from the appointment's.
func TestScheduleReminder_AcrossDaylightSaving(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	require.NoError(t, err)
	for _, c := range []struct {
		start, reminder time.Time
	}{
		// Clocks go forward at 02:00 on Sunday 4 October 2026.
		{time.Date(2026, 10, 4, 10, 0, 0, 0, sydney), time.Date(2026, 10, 3, 9, 0, 0, 0, sydney)},
		// Clocks go back at 03:00 on Sunday 5 April 2026.
		{time.Date(2026, 4, 5, 10, 0, 0, 0, sydney), time.Date(2026, 4, 4, 11, 0, 0, 0, sydney)},
	} {
		// Cancelled keeps the fixed date out of the overlap constraint, so it
		// cannot collide with other tests' appointments.
		appt, err := insert(t, appointment(t, booking.CancelledByPractitioner, c.start))
		require.NoError(t, err)
		_, err = booking.ScheduleReminder(context.Background(), db.New(pgtest.Pool(t)), appt, 24,
			c.start.Add(-72*time.Hour))
		require.NoError(t, err)
		got := remindersOf(t, appt.ID)
		require.Len(t, got, 1)
		require.Equal(t, 24*time.Hour, c.start.Sub(got[0].scheduledFor))
		require.Equal(t, c.reminder.Format(time.RFC3339), got[0].scheduledFor.In(sydney).Format(time.RFC3339))
	}
}

// A reschedule cancels the old reminder and queues one for the new time.
func TestCancelReminders_OnReschedule(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	appt, err := insert(t, appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour)))
	require.NoError(t, err)
	now := appt.StartsAt.Add(-72 * time.Hour)
	_, err = booking.ScheduleReminder(ctx, q, appt, 24, now)
	require.NoError(t, err)

	require.NoError(t, booking.CancelReminders(ctx, q, appt.ID, comms.SkipSuperseded))
	appt.StartsAt = appt.StartsAt.Add(2 * time.Hour)
	_, err = booking.ScheduleReminder(ctx, q, appt, 24, now)
	require.NoError(t, err)

	got := remindersOf(t, appt.ID)
	require.Len(t, got, 2)
	require.Equal(t, "cancelled", got[0].status)
	require.Equal(t, "queued", got[1].status)
	require.True(t, appt.StartsAt.Add(-24*time.Hour).Equal(got[1].scheduledFor))
}
