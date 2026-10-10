package pgtest

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// TestMain also checks that Run dropped the shared database.
func TestMain(m *testing.M) {
	code := Run(m)
	if code == 0 && sharedDB != "" && exists(sharedDB) {
		fmt.Fprintln(os.Stderr, "pgtest: the shared database still exists after Run")
		code = 1
	}
	os.Exit(code)
}

func exists(dbURL string) bool {
	cfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		panic(err)
	}
	name := cfg.Database
	cfg.Database = "postgres"
	ctx := context.Background()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer conn.Close(ctx)
	var found bool
	err = conn.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&found)
	if err != nil {
		panic(err)
	}
	return found
}

func TestPoolCallsShareOneMigratedDatabase(t *testing.T) {
	ctx := context.Background()
	var first, second string
	require.NoError(t, Pool(t).QueryRow(ctx, "SELECT current_database()").Scan(&first))
	require.NoError(t, Pool(t).QueryRow(ctx, "SELECT current_database()").Scan(&second))
	require.Equal(t, first, second)
	require.True(t, strings.HasPrefix(first, "vetmimi_test_"), first)

	var applied bool
	require.NoError(t, Pool(t).QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM goose_db_version WHERE version_id = 1)").Scan(&applied))
	require.True(t, applied)
}

func TestEmptyDatabaseIsUnmigratedAndDroppedAfterTheTest(t *testing.T) {
	var dbURL string
	t.Run("use", func(t *testing.T) {
		dbURL = EmptyDatabase(t)
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, dbURL)
		require.NoError(t, err)
		defer conn.Close(ctx)
		var migrated bool
		require.NoError(t, conn.QueryRow(ctx, "SELECT to_regclass('goose_db_version') IS NOT NULL").Scan(&migrated))
		require.False(t, migrated)
	})
	require.False(t, exists(dbURL), "the database outlived its test")
}

func TestMissingURLFailsInsteadOfSkipping(t *testing.T) {
	t.Setenv(envURL, "")
	for name, open := range map[string]func(testing.TB){
		"Pool":          func(t testing.TB) { Pool(t) },
		"EmptyDatabase": func(t testing.TB) { EmptyDatabase(t) },
	} {
		r := stopped(t, open)
		require.Equal(t, "fatal", r.how, name)
		require.Contains(t, r.msg, "DATABASE_URL_TEST is not set", name)
	}
}

func TestUseWithoutRunFails(t *testing.T) {
	running = false
	t.Cleanup(func() { running = true })
	r := stopped(t, func(t testing.TB) { Pool(t) })
	require.Equal(t, "fatal", r.how)
	require.Contains(t, r.msg, "pgtest.Run")
}

// recorder records whether Pool called Fatal or Skip, ending the goroutine as
// a real testing.T does.
type recorder struct {
	testing.TB
	how, msg string
}

func (r *recorder) Fatal(args ...any) { r.stop("fatal", fmt.Sprint(args...)) }
func (r *recorder) Skip(args ...any)  { r.stop("skip", fmt.Sprint(args...)) }
func (r *recorder) SkipNow()          { r.stop("skip", "") }
func (r *recorder) Skipf(format string, args ...any) {
	r.stop("skip", fmt.Sprintf(format, args...))
}

func (r *recorder) stop(how, msg string) {
	r.how, r.msg = how, msg
	runtime.Goexit()
}

func stopped(t *testing.T, fn func(testing.TB)) *recorder {
	r := &recorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	return r
}
