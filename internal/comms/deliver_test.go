package comms_test

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
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

// Two workers given the same task send one email: the row lock is skipped
// by the second, and the status check stops any later run.
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
