package booking_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/migrations"
)

// The database's services_duration_required decides, not a Go check.
func TestCreateService_RequestNeedsDuration(t *testing.T) {
	_, err := booking.CreateService(context.Background(), db.New(pgtest.Pool(t)), db.CreateServiceParams{
		Slug: "no-duration", Name: []byte(`{"en": "No duration"}`), BookingAction: "request",
	})
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/durationMinutes", e.Fields[0].Field)
}

func TestResumeService(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	free, err := q.GetServiceBySlug(ctx, "free-consultation")
	require.NoError(t, err)
	require.Equal(t, "paused", free.State)

	resumed, err := booking.SetServiceState(ctx, q, free.ID, free.Version, time.Now(), "active")
	require.NoError(t, err)
	require.Equal(t, "active", resumed.State)
	require.Equal(t, free.Version+1, resumed.Version)
	t.Cleanup(func() {
		_, err := booking.SetServiceState(ctx, q, free.ID, resumed.Version, time.Now(), "paused")
		require.NoError(t, err)
	})

	_, err = booking.SetServiceState(ctx, q, free.ID, resumed.Version, time.Now(), "active")
	requireCode(t, apperr.InvalidTransition, err)
	_, err = booking.SetServiceState(ctx, q, free.ID, free.Version, time.Now(), "paused")
	requireCode(t, apperr.StaleVersion, err)
}

// New durations apply to future bookings only: an appointment keeps the
// busy range it was booked with.
func TestUpdateServiceLeavesAppointmentsAlone(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	svc, err := booking.CreateService(ctx, q, db.CreateServiceParams{
		Slug: "longer-later", Name: []byte(`{"en": "Longer later"}`), BookingAction: "request",
		DurationMinutes: pgtype.Int4{Int32: 50, Valid: true}, BufferAfterMinutes: 10,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM appointments WHERE service_id = $1", svc.ID)
		require.NoError(t, err)
		require.NoError(t, booking.DeleteService(ctx, q, svc.ID))
	})
	a := appointment(t, booking.Confirmed, freeDay().Add(9*time.Hour))
	a.Service = svc
	booked, err := insert(t, a)
	require.NoError(t, err)
	before := rowJSON(t, booked.ID)

	_, err = booking.UpdateService(ctx, q, svc.ID, svc.Version, time.Now(), func(p *db.UpdateServiceParams) {
		p.DurationMinutes = pgtype.Int4{Int32: 90, Valid: true}
		p.BufferAfterMinutes = 30
	})
	require.NoError(t, err)

	require.Equal(t, before, rowJSON(t, booked.ID))

	requireCode(t, apperr.InUse, booking.DeleteService(ctx, q, svc.ID))
}

// setBookingEnabled stores public_booking_enabled and restores it after t.
func setBookingEnabled(t *testing.T, enabled bool) {
	t.Helper()
	set := func(v bool) {
		_, err := pgtest.Pool(t).Exec(context.Background(),
			"UPDATE settings SET value = to_jsonb($1::boolean) WHERE key = 'public_booking_enabled'", v)
		require.NoError(t, err)
	}
	set(enabled)
	t.Cleanup(func() { set(true) })
}

func TestPublicServices_HidesPaused(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	// Seeded: individual-art-therapy (request), free-consultation (paused)
	// and two enquiry-only services.
	svc, err := booking.CreateService(ctx, q, db.CreateServiceParams{
		Slug: "studio-session", Name: []byte(`{"en": "Studio session", "my": "စတူဒီယို"}`),
		Description: []byte(`{"en": "Two hours in the studio"}`), FeeText: []byte(`{"en": "$120", "my": "၁၂၀"}`),
		BookingAction: "book", DurationMinutes: pgtype.Int4{Int32: 120, Valid: true}, BufferBeforeMinutes: 10, SortOrder: 9,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, booking.DeleteService(ctx, q, svc.ID)) })

	list, err := booking.ListPublicServices(ctx, q, "my")
	require.NoError(t, err)
	require.True(t, list.BookingEnabled)
	require.Equal(t, "request_approval", list.BookingMode)
	require.Equal(t, "Australia/Sydney", list.Timezone)
	require.Len(t, list.Items, 2)
	require.Equal(t, "individual-art-therapy", list.Items[0].Slug)
	require.Equal(t, booking.PublicService{
		Slug: "studio-session", Name: "စတူဒီယို", Description: "Two hours in the studio", BookingAction: "book",
		DurationMinutes: 120, Formats: []string{"online"}, FeeText: "၁၂၀",
	}, list.Items[1], "Burmese where it is written, English where it is not")
}

func TestPublicServices_EmptyWhileBookingPaused(t *testing.T) {
	setBookingEnabled(t, false)
	list, err := booking.ListPublicServices(context.Background(), db.New(pgtest.Pool(t)), "en")
	require.NoError(t, err)
	require.False(t, list.BookingEnabled)
	require.Empty(t, list.Items)

	therapy := therapy(t)
	require.Equal(t, "active", therapy.State, "pausing booking leaves services alone")
}

func TestFourLaunchServicesAreSeeded(t *testing.T) {
	services, err := db.New(pgtest.Pool(t)).ListServices(context.Background())
	require.NoError(t, err)

	type seed struct {
		slug, action, state string
		duration            int32
		before, after       int32
	}
	var got []seed
	var bookable []string
	for _, s := range services {
		got = append(got, seed{s.Slug, s.BookingAction, s.State, s.DurationMinutes.Int32,
			s.BufferBeforeMinutes, s.BufferAfterMinutes})
		require.Equal(t, []string{"online"}, s.Formats, s.Slug)
		require.Nil(t, s.Description, s.Slug)
		require.Nil(t, s.FeeText, s.Slug)
		if s.State == "active" && (s.BookingAction == "book" || s.BookingAction == "request") {
			bookable = append(bookable, s.Slug)
		}
	}
	require.Equal(t, []seed{
		{"individual-art-therapy", "request", "active", 60, 0, 15},
		{"group-art-wellbeing", "enquiry_only", "active", 0, 0, 0},
		{"workshops-programs", "enquiry_only", "active", 0, 0, 0},
		{"free-consultation", "request", "paused", 20, 0, 0},
	}, got)
	require.Equal(t, []string{"individual-art-therapy"}, bookable)

	var name map[string]string
	require.NoError(t, json.Unmarshal(services[0].Name, &name))
	require.Equal(t, "Individual Art Therapy", name["en"])
	require.NotEmpty(t, name["my"])
}

// seedInsert is the migration's INSERT, so a test can run it again.
func seedInsert(t *testing.T) string {
	t.Helper()
	body, err := migrations.FS.ReadFile("00005_create_services.sql")
	require.NoError(t, err)
	sql := string(body)
	start := strings.Index(sql, "INSERT INTO services")
	require.Positive(t, start)
	end := strings.Index(sql[start:], ";")
	return sql[start : start+end]
}

// An edit made in admin survives the seed running again.
func TestSeedDoesNotOverwriteEdits(t *testing.T) {
	pool := pgtest.Pool(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, "UPDATE services SET duration_minutes = 50 WHERE slug = 'individual-art-therapy'")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "UPDATE services SET duration_minutes = 60 WHERE slug = 'individual-art-therapy'")
		require.NoError(t, err)
	})

	_, err = pool.Exec(ctx, seedInsert(t))
	require.NoError(t, err)
	s, err := db.New(pool).GetServiceBySlug(ctx, "individual-art-therapy")
	require.NoError(t, err)
	require.Equal(t, int32(50), s.DurationMinutes.Int32)
}

func TestServicesChecks(t *testing.T) {
	pool := pgtest.Pool(t)
	for constraint, insert := range map[string]string{
		"services_duration_required": `INSERT INTO services (slug, name, booking_action) VALUES ('no-duration', '{"en": "x"}', 'request')`,
		"services_slug_format":       `INSERT INTO services (slug, name, duration_minutes) VALUES ('Bad Slug', '{"en": "x"}', 30)`,
		"services_name_en":           `INSERT INTO services (slug, name, duration_minutes) VALUES ('no-english', '{"my": "x"}', 30)`,
	} {
		_, err := pool.Exec(context.Background(), insert)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, constraint)
		require.Equal(t, constraint, pgErr.ConstraintName)
	}
}
