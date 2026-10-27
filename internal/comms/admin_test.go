package comms_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func TestResend_QueuesACopyToTheCurrentAddress(t *testing.T) {
	appt := newAppointment(t, freeStart())
	orig, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	fail(t, orig.ID)
	require.True(t, failedEmailShows(t, appt.ID))
	exec(t, "UPDATE appointments SET visitor_email = 'fixed@example.com' WHERE id = $1", appt.ID)
	admin := practitioner(t)

	copied, task, err := comms.Resend(context.Background(), pgtest.Pool(t), appt.ID, orig.ID, admin, time.Now())
	require.NoError(t, err)

	require.Equal(t, "queued", copied.Status)
	require.Equal(t, orig.ID, copied.ResendOf)
	require.Equal(t, admin, copied.CreatedBy)
	require.Equal(t, "fixed@example.com", copied.Recipient.String)
	require.Equal(t, "failed", row(t, orig.ID).Status, "the original stays as it was")

	resend := newFakeResend(t, http.StatusOK)
	tasks, _ := newTasks(t, resend, time.Now())
	require.NoError(t, deliver(t, tasks, task))
	require.Equal(t, "sent", row(t, copied.ID).Status)
	require.Equal(t, []any{"fixed@example.com"}, resend.sent()[0].Body["to"])
	require.False(t, failedEmailShows(t, appt.ID), "a sent copy clears the dashboard")
}

func TestResend_SendsAReminderAtOnce(t *testing.T) {
	appt := newAppointment(t, freeStart())
	orig, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail, Locale: "en",
		ScheduledFor: appt.StartsAt.Add(-24 * time.Hour)})
	fail(t, orig.ID)

	copied, task, err := comms.Resend(context.Background(), pgtest.Pool(t), appt.ID, orig.ID, practitioner(t), time.Now())
	require.NoError(t, err)
	tasks, _ := newTasks(t, newFakeResend(t, http.StatusOK), time.Now())
	require.NoError(t, deliver(t, tasks, task))
	require.Equal(t, "sent", row(t, copied.ID).Status, "a resent reminder is not skipped as superseded")
}

func TestResend_Refusals(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	appt := newAppointment(t, freeStart())
	other := newAppointment(t, freeStart())
	queued, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	failed, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived, Recipient: visitorEmail, Locale: "en"})
	fail(t, failed.ID)

	_, _, err := comms.Resend(ctx, pool, appt.ID, queued.ID, practitioner(t), time.Now())
	requireAppErr(t, apperr.ActionNotAllowed, err)
	_, _, err = comms.Resend(ctx, pool, other.ID, failed.ID, practitioner(t), time.Now())
	requireAppErr(t, apperr.NotFound, err)

	reminder, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail, Locale: "en"})
	fail(t, reminder.ID)
	exec(t, "UPDATE appointments SET status = 'cancelled_by_client' WHERE id = $1", appt.ID)
	_, _, err = comms.Resend(ctx, pool, appt.ID, reminder.ID, practitioner(t), time.Now())
	requireAppErr(t, apperr.ActionNotAllowed, err)
}

func TestMarkCommunicated_RecordsAPhoneCall(t *testing.T) {
	appt := newAppointment(t, freeStart())
	orig, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	fail(t, orig.ID)
	admin := practitioner(t)
	now := time.Now().UTC().Truncate(time.Microsecond)

	got, err := comms.MarkCommunicated(context.Background(), db.New(pgtest.Pool(t)), appt.ID, comms.BookingConfirmed,
		"Phoned to confirm", admin, now)
	require.NoError(t, err)

	require.Equal(t, "manual", got.Channel)
	require.Equal(t, "sent", got.Status)
	require.Equal(t, "Phoned to confirm", got.Note.String)
	require.Equal(t, admin, got.CreatedBy)
	require.False(t, got.Recipient.Valid)
	require.True(t, got.SentAt.Time.Equal(now))
	require.False(t, failedEmailShows(t, appt.ID), "the call clears the failed email from the dashboard")
}

func TestMarkCommunicated_UnknownAppointment(t *testing.T) {
	missing := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	_, err := comms.MarkCommunicated(context.Background(), db.New(pgtest.Pool(t)), missing, comms.BookingConfirmed,
		"Phoned", practitioner(t), time.Now())
	requireAppErr(t, apperr.NotFound, err)
}

func fail(t *testing.T, id pgtype.UUID) {
	t.Helper()
	exec(t, "UPDATE communications SET status = 'failed', error = 'provider_error', attempts = 5 WHERE id = $1", id)
}

func practitioner(t *testing.T) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), "SELECT id FROM users WHERE is_practitioner").Scan(&id))
	return id
}

func failedEmailShows(t *testing.T, appointmentID pgtype.UUID) bool {
	t.Helper()
	now := time.Now()
	items, err := db.New(pgtest.Pool(t)).ListAttention(context.Background(), db.ListAttentionParams{Now: now, HoldHorizon: now})
	require.NoError(t, err)
	for _, item := range items {
		if item.Kind == "failed_communication" && item.ID == appointmentID {
			return true
		}
	}
	return false
}
