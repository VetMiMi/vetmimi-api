package comms_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

func TestDeliver_SendsAndRecords(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	resend := newFakeResend(t, http.StatusOK)
	tasks, logs := newTasks(t, resend, time.Now())

	require.NoError(t, deliver(t, tasks, task))

	sent := resend.sent()
	require.Len(t, sent, 1)
	require.Equal(t, r.ID.String(), sent[0].IdempotencyKey)
	require.Equal(t, []any{visitorEmail}, sent[0].Body["to"])
	require.Equal(t, "VetMiMi <hello@example.com>", sent[0].Body["from"])
	require.Equal(t, "meenaerie@gmail.com", sent[0].Body["reply_to"], "Reply-To is settings.contact_email")
	require.Contains(t, sent[0].Body["subject"], "Confirmed: Individual Art Therapy")
	got := row(t, r.ID)
	require.Equal(t, "sent", got.Status)
	require.Equal(t, "re_123", got.ProviderMessageID.String)
	require.EqualValues(t, 1, got.Attempts)
	require.True(t, got.SentAt.Valid)
	require.Contains(t, logs.String(), `"msg":"email sent"`)
	require.NotContains(t, logs.String(), visitorEmail)
}

func TestDeliver_FailsAfterFiveAttempts(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived, Recipient: visitorEmail, Locale: "en"})
	resend := newFakeResend(t, http.StatusInternalServerError)
	tasks, logs := newTasks(t, resend, time.Now())
	failedBefore := comms.EmailsFailed.Value()

	for range 4 {
		err := deliver(t, tasks, task)
		require.Error(t, err, "a provider error is retried")
		require.NotContains(t, err.Error(), visitorEmail)
		require.Equal(t, "queued", row(t, r.ID).Status)
	}
	require.NoError(t, deliver(t, tasks, task), "the last attempt ends the retries")

	got := row(t, r.ID)
	require.Equal(t, "failed", got.Status)
	require.EqualValues(t, 5, got.Attempts)
	require.Equal(t, "provider_error", got.Error.String)
	require.Equal(t, failedBefore+1, comms.EmailsFailed.Value())
	require.Len(t, resend.sent(), 5)
	for _, s := range resend.sent() {
		require.Equal(t, r.ID.String(), s.IdempotencyKey, "every retry uses the same key")
	}
	require.NotContains(t, logs.String(), visitorEmail)

	require.NoError(t, deliver(t, tasks, task))
	require.Len(t, resend.sent(), 5, "a failed row is never sent again")
}

func TestDeliver_SkipsSentAndCancelledRows(t *testing.T) {
	appt := newAppointment(t, freeStart())
	sent, sentTask := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	exec(t, "UPDATE communications SET status = 'sent', sent_at = now() WHERE id = $1", sent.ID)
	_, cancelledTask := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail, Locale: "en"})
	exec(t, "UPDATE communications SET status = 'cancelled' WHERE kind = 'reminder' AND appointment_id = $1", appt.ID)
	resend := newFakeResend(t, http.StatusOK)
	tasks, _ := newTasks(t, resend, time.Now())

	require.NoError(t, deliver(t, tasks, sentTask))
	require.NoError(t, deliver(t, tasks, cancelledTask))
	require.Empty(t, resend.sent())
}

func TestDeliver_TwoWorkersSendOnce(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	resend := newFakeResend(t, http.StatusOK)
	resend.delay = 200 * time.Millisecond
	tasks, _ := newTasks(t, resend, time.Now())

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { require.NoError(t, deliver(t, tasks, task)) })
	}
	wg.Wait()
	require.NoError(t, deliver(t, tasks, task))

	require.Len(t, resend.sent(), 1)
	require.Equal(t, "sent", row(t, r.ID).Status)
}

func TestDeliver_DevelopmentLogsInsteadOfSending(t *testing.T) {
	appt := newAppointment(t, freeStart())
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived, Recipient: visitorEmail, Locale: "my"})
	tasks, logs := newTasks(t, nil, time.Now())

	require.NoError(t, deliver(t, tasks, task))

	got := row(t, r.ID)
	require.Equal(t, "sent", got.Status)
	require.Equal(t, "dev", got.ProviderMessageID.String)
	require.Contains(t, logs.String(), `"msg":"would send","kind":"request_received","communication_id":"`+r.ID.String()+`"`)
	require.NotContains(t, logs.String(), visitorEmail)
	require.NotContains(t, logs.String(), "Visitor")
}

func TestDeliver_SendsDawMisMessage(t *testing.T) {
	appt := newAppointment(t, freeStart())
	message := "I am away that week; please choose another."
	r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled, Recipient: visitorEmail,
		Locale: "en", Text: message})
	require.Equal(t, message, r.Message.String)
	resend := newFakeResend(t, http.StatusOK)
	tasks, logs := newTasks(t, resend, time.Now())

	require.NoError(t, deliver(t, tasks, task))

	sent := resend.sent()
	require.Len(t, sent, 1)
	require.Contains(t, sent[0].Body["text"], message)
	require.Contains(t, sent[0].Body["html"], "I am away that week; please choose another.")
	require.NotContains(t, logs.String(), message)
}

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

// reminderAt queues a reminder that is due now.
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
