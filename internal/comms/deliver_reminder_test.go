package comms_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

func TestReminderSkip(t *testing.T) {
	now := time.Date(2032, 3, 1, 0, 0, 0, 0, time.UTC)
	start := now.Add(20 * time.Hour)
	due := start.Add(-24 * time.Hour)
	appt := func(status string, startsAt time.Time) db.GetAppointmentForMessageRow {
		return db.GetAppointmentForMessageRow{Status: status, StartsAt: startsAt}
	}
	require.Empty(t, comms.ReminderSkip(appt("confirmed", start), 24, due, now))
	require.Equal(t, comms.SkipNotConfirmed, comms.ReminderSkip(appt("cancelled_by_client", start), 24, due, now))
	require.Equal(t, comms.SkipNotConfirmed, comms.ReminderSkip(appt("pending", start), 24, due, now))
	require.Equal(t, comms.SkipAlreadyStarted, comms.ReminderSkip(appt("confirmed", now), 24, now.Add(-24*time.Hour), now))
	require.Equal(t, comms.SkipSuperseded, comms.ReminderSkip(appt("confirmed", start.Add(time.Hour)), 24, due, now),
		"a rescheduled appointment's old reminder")
	require.Equal(t, comms.SkipSuperseded, comms.ReminderSkip(appt("confirmed", start), 48, due, now),
		"a reminder from before reminder_hours changed")
}

// reminderAt queues the reminder of a confirmed appointment at its start less
// the default 24 hours, and returns the clock at that moment.
func reminderAt(t *testing.T) (db.Appointment, db.Communication, func() error, *fakeResend) {
	t.Helper()
	appt := newAppointment(t, freeStart())
	due := appt.StartsAt.Add(-24 * time.Hour)
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail, Locale: "en", ScheduledFor: due})
	resend := newFakeResend(t, http.StatusOK)
	tasks, _ := newTasks(t, resend, due)
	return appt, r, func() error { return deliver(t, tasks, task) }, resend
}

func TestReminder_SentWhenStillConfirmed(t *testing.T) {
	_, r, fire, resend := reminderAt(t)
	require.NoError(t, fire())
	require.Len(t, resend.sent(), 1)
	require.Equal(t, "sent", row(t, r.ID).Status)
}

func TestReminder_SkippedWhenCancelled(t *testing.T) {
	appt, r, fire, resend := reminderAt(t)
	exec(t, "UPDATE appointments SET status = 'cancelled_by_client' WHERE id = $1", appt.ID)
	require.NoError(t, fire())
	require.Empty(t, resend.sent())
	got := row(t, r.ID)
	require.Equal(t, "cancelled", got.Status)
	require.Equal(t, comms.SkipNotConfirmed, got.Error.String)
}

func TestReminder_SkippedWhenRescheduled(t *testing.T) {
	appt, r, fire, resend := reminderAt(t)
	exec(t, "UPDATE appointments SET starts_at = starts_at + interval '2 hours', ends_at = ends_at + interval '2 hours', "+
		"busy_range = tstzrange(lower(busy_range) + interval '2 hours', upper(busy_range) + interval '2 hours') WHERE id = $1",
		appt.ID)
	require.NoError(t, fire())
	require.Empty(t, resend.sent())
	require.Equal(t, comms.SkipSuperseded, row(t, r.ID).Error.String)
}

// A reminder moved later by a reminder_hours change is not sent by the task
// that still holds its old time.
func TestReminder_NotSentBeforeItsTime(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail,
		Locale: "en", ScheduledFor: appt.StartsAt.Add(-24 * time.Hour)})
	resend := newFakeResend(t, http.StatusOK)
	tasks, _ := newTasks(t, resend, appt.StartsAt.Add(-48*time.Hour))
	require.NoError(t, deliver(t, tasks, task))
	require.Empty(t, resend.sent())
	require.Equal(t, "queued", row(t, r.ID).Status)
}
