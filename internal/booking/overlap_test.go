package booking_test

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// days hands each test its own day, far in the future, so tests sharing the
// database never overlap each other's appointments.
var days atomic.Int64

func freeDay() time.Time {
	return time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(days.Add(1)))
}

// practitioner is the one user appointments are booked with, created once.
func practitioner(t *testing.T) pgtype.UUID {
	t.Helper()
	pool := pgtest.Pool(t)
	var id pgtype.UUID
	err := pool.QueryRow(context.Background(), "SELECT id FROM users WHERE is_practitioner").Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id, err = db.New(pool).CreateUser(context.Background(), db.CreateUserParams{
			Email: "mi@example.com", DisplayName: "Daw Mi", PasswordHash: "x",
			Roles: []string{"site_admin"}, IsPractitioner: true, TotpSecretEnc: []byte("sealed"),
		})
	}
	require.NoError(t, err)
	return id
}

// therapy is the seeded 60-minute service with a 15-minute buffer after.
func therapy(t *testing.T) db.Service {
	t.Helper()
	s, err := db.New(pgtest.Pool(t)).GetServiceBySlug(context.Background(), "individual-art-therapy")
	require.NoError(t, err)
	return s
}

func appointment(t *testing.T, status booking.Status, startsAt time.Time) booking.NewAppointment {
	t.Helper()
	ack := sql.NullTime{Time: startsAt.Add(-48 * time.Hour), Valid: true}
	a := booking.NewAppointment{
		PractitionerID: practitioner(t),
		Service:        therapy(t),
		StartsAt:       startsAt,
		Duration:       time.Hour,
		Status:         status,
		Timezone:       "Australia/Sydney",
		Format:         "online",
		Locale:         "en",
		Source:         "website",
		VisitorName:    "Visitor",
		VisitorEmail:   "visitor@example.com",
		PrivacyAckAt:   ack,
		PolicyAckAt:    ack,
	}
	if status == booking.Pending {
		a.HoldExpiresAt = sql.NullTime{Time: startsAt.Add(-time.Hour), Valid: true}
	}
	return a
}

func insert(t *testing.T, a booking.NewAppointment) (db.Appointment, error) {
	t.Helper()
	return booking.InsertAppointment(context.Background(), db.New(pgtest.Pool(t)), testSecret, a)
}

// rowJSON is an appointment row exactly as stored, to show nothing changed it.
func rowJSON(t *testing.T, id pgtype.UUID) string {
	t.Helper()
	var row string
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT to_jsonb(a)::text FROM appointments a WHERE id = $1", id).Scan(&row))
	return row
}

func requireCode(t *testing.T, code apperr.Code, err error) *apperr.Error {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, e.Detail)
	return e
}

func TestOverlap_DisjointRowsAndCancelledRowsSave(t *testing.T) {
	day := freeDay()
	pending, err := insert(t, appointment(t, booking.Pending, day.Add(9*time.Hour)))
	require.NoError(t, err)
	require.Regexp(t, referencePattern, pending.Reference)
	require.Equal(t, day.Add(10*time.Hour), pending.EndsAt.UTC())
	require.Equal(t, day.Add(10*time.Hour+15*time.Minute), booking.PeriodOf(pending.BusyRange).End.UTC(),
		"busy range carries the after buffer")

	_, err = insert(t, appointment(t, booking.Confirmed, day.Add(11*time.Hour)))
	require.NoError(t, err)
	_, err = insert(t, appointment(t, booking.CancelledByClient, day.Add(11*time.Hour)))
	require.NoError(t, err, "a cancelled row falls out of the constraint")
}

func TestOverlap_PendingOverConfirmedIsSlotUnavailable(t *testing.T) {
	day := freeDay()
	_, err := insert(t, appointment(t, booking.Confirmed, day.Add(9*time.Hour)))
	require.NoError(t, err)

	_, err = insert(t, appointment(t, booking.Pending, day.Add(9*time.Hour+30*time.Minute)))
	requireCode(t, apperr.SlotUnavailable, err)
	// Starting inside the confirmed session's after buffer is refused too.
	_, err = insert(t, appointment(t, booking.Pending, day.Add(10*time.Hour+10*time.Minute)))
	requireCode(t, apperr.SlotUnavailable, err)
}

func TestOverlap_WebsiteRequestNeedsAcknowledgements(t *testing.T) {
	a := appointment(t, booking.Pending, freeDay().Add(9*time.Hour))
	a.PrivacyAckAt = sql.NullTime{}
	_, err := insert(t, a)
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "appointments_website_acknowledged", pgErr.ConstraintName)
}

// ADR-004: two requests for one slot in separate transactions, at once;
// the exclusion constraint lets exactly one commit.
func TestOverlap_TwoWritersOneWinner(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	for range 10 {
		a := appointment(t, booking.Pending, freeDay().Add(9*time.Hour))
		var wg sync.WaitGroup
		errs := make([]error, 2)
		start := make(chan struct{})
		for i := range errs {
			wg.Go(func() {
				<-start
				errs[i] = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
					_, err := booking.InsertAppointment(ctx, db.New(tx), testSecret, a)
					return err
				})
			})
		}
		close(start)
		wg.Wait()

		failed := 0
		for _, err := range errs {
			if err == nil {
				continue
			}
			failed++
			// Two inserts checking the constraint at once can each wait on
			// the other; PostgreSQL then aborts one as a deadlock. Booking
			// takes the schedule lock first, so only this test sees that.
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "40P01" {
				requireCode(t, apperr.SlotUnavailable, err)
			}
		}
		require.Equal(t, 1, failed, "exactly one writer wins")
	}
}
