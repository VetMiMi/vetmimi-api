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
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// ended stores an appointment in status that ended at end, with a history
// entry and a message.
func ended(t *testing.T, status booking.Status, end time.Time) pgtype.UUID {
	t.Helper()
	ctx := context.Background()
	appt, err := insert(t, appointment(t, booking.Confirmed, end.Add(-time.Hour)))
	require.NoError(t, err)
	q := db.New(pgtest.Pool(t))
	require.NoError(t, booking.AppendEvent(ctx, q, booking.Event{AppointmentID: appt.ID, Kind: "created", Actor: "visitor"}))
	_, err = comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed,
		Recipient: "visitor@example.com", Locale: "en"})
	require.NoError(t, err)
	if status != booking.Confirmed {
		_, err = q.SetAppointmentStatus(ctx, db.SetAppointmentStatusParams{ID: appt.ID, Status: string(status), Now: end})
		require.NoError(t, err)
	}
	return appt.ID
}

func enquiryCreatedAt(t *testing.T, at time.Time) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), `INSERT INTO contact_enquiries
		(reference, name, email, enquiry_type, message, privacy_ack_at, created_at)
		VALUES ('EN-' || substr(md5(random()::text), 1, 6), 'Visitor', 'visitor@example.com', 'general', 'Hello', $1, $1)
		RETURNING id`, at).Scan(&id))
	return id
}

func exists(t *testing.T, table string, id pgtype.UUID) bool {
	return countRows(t, "SELECT count(*) FROM "+table+" WHERE id = $1", id) == 1
}

// Every row here is in 2020 and the clock reads 2022, so the rest of the
// package's rows, all later, are never old enough for this purge.
func TestPurge_DeletesPastRetentionAndKeepsTheRest(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2022, 6, 15, 12, 0, 0, 0, time.UTC)
	old := ended(t, booking.Completed, now.AddDate(0, -25, 0))
	recent := ended(t, booking.Completed, now.AddDate(0, -23, 0))
	active := ended(t, booking.Confirmed, now.AddDate(0, -26, 0))
	oldEnquiry := enquiryCreatedAt(t, now.AddDate(0, -25, 0))
	newEnquiry := enquiryCreatedAt(t, now.AddDate(0, -23, 0))
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM appointments WHERE id = ANY($1)", []pgtype.UUID{recent, active})
		require.NoError(t, err)
		_, err = pgtest.Pool(t).Exec(ctx, "DELETE FROM contact_enquiries WHERE id = $1", newEnquiry)
		require.NoError(t, err)
	})

	purged, err := booking.Purge(ctx, db.New(pgtest.Pool(t)), now)
	require.NoError(t, err)
	require.Equal(t, booking.Purged{Appointments: 1, Enquiries: 1}, purged)
	require.False(t, exists(t, "appointments", old))
	require.Zero(t, countRows(t, "SELECT count(*) FROM appointment_events WHERE appointment_id = $1", old))
	require.Zero(t, countRows(t, "SELECT count(*) FROM communications WHERE appointment_id = $1", old))
	require.True(t, exists(t, "appointments", recent))
	require.True(t, exists(t, "appointments", active), "a confirmed appointment is never deleted")
	require.False(t, exists(t, "contact_enquiries", oldEnquiry))
	require.True(t, exists(t, "contact_enquiries", newEnquiry))

	again, err := booking.Purge(ctx, db.New(pgtest.Pool(t)), now)
	require.NoError(t, err)
	require.Equal(t, booking.Purged{}, again, "a second run deletes nothing")

	setSetting(t, "retention_months", "12")
	_, err = booking.Purge(ctx, db.New(pgtest.Pool(t)), now)
	require.NoError(t, err)
	require.False(t, exists(t, "appointments", recent), "a shorter retention takes effect on the next run")
	require.False(t, exists(t, "contact_enquiries", newEnquiry))
}
