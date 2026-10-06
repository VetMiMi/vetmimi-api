package booking_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
	"github.com/VetMiMi/vetmimi-api/migrations"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

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
