package platform_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

func open(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := platform.OpenPostgres(context.Background(), url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// versions counts the rows goose recorded for each migration version.
func versions(t *testing.T, pool *pgxpool.Pool) map[int64]int {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT version_id, count(*) FROM goose_db_version GROUP BY version_id")
	require.NoError(t, err)
	defer rows.Close()
	counts := map[int64]int{}
	for rows.Next() {
		var version int64
		var n int
		require.NoError(t, rows.Scan(&version, &n))
		counts[version] = n
	}
	require.NoError(t, rows.Err())
	return counts
}

func TestOpenPostgresSizesThePoolForTheLiveHost(t *testing.T) {
	cfg := pgtest.Pool(t).Config()
	require.EqualValues(t, 10, cfg.MaxConns)
	require.Equal(t, 5*time.Second, cfg.ConnConfig.ConnectTimeout)
}

func TestOpenPostgresNamesTheHostButNotThePassword(t *testing.T) {
	for name, url := range map[string]string{
		"unreachable": "postgres://vetmimi:hunter2-secret@127.0.0.1:1/vetmimi?sslmode=disable",
		"invalid":     "postgres://vetmimi:hunter2-secret@127.0.0.1:notaport/vetmimi",
	} {
		_, err := platform.OpenPostgres(context.Background(), url)
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), "hunter2-secret", name)
		if name == "unreachable" {
			require.Contains(t, err.Error(), "127.0.0.1:1", name)
		}
	}
}

func TestMigrateAppliesPendingMigrationsOnce(t *testing.T) {
	pool := open(t, pgtest.EmptyDatabase(t))
	ctx := context.Background()

	require.NoError(t, platform.Migrate(ctx, pool))
	first := versions(t, pool)
	require.Equal(t, 1, first[1], "00001_extensions applied once")

	var extensions int
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT count(*) FROM pg_extension WHERE extname IN ('btree_gist', 'pgcrypto')").Scan(&extensions))
	require.Equal(t, 2, extensions)

	require.NoError(t, platform.Migrate(ctx, pool))
	require.Equal(t, first, versions(t, pool), "a second run changes nothing")
}

// The api and worker containers start together on every deploy, each with its
// own pool, and both migrate.
func TestConcurrentMigrateAppliesOnce(t *testing.T) {
	url := pgtest.EmptyDatabase(t)
	pools := []*pgxpool.Pool{open(t, url), open(t, url)}

	// goose retries creating its version table a second later when another
	// process wins that race, which would serialise this test by accident.
	// Creating the table first is the state a container meets when it starts
	// while the other is mid-migration, and puts both at 00001 together.
	ctx := context.Background()
	store, err := database.NewStore(goose.DialectPostgres, "goose_db_version")
	require.NoError(t, err)
	db := stdlib.OpenDBFromPool(pools[0])
	require.NoError(t, store.CreateVersionTable(ctx, db))
	require.NoError(t, store.Insert(ctx, db, database.InsertRequest{Version: 0}))

	start := make(chan struct{})
	errs := make([]error, len(pools))
	var wg sync.WaitGroup
	for i, pool := range pools {
		wg.Go(func() {
			<-start
			errs[i] = platform.Migrate(ctx, pool)
		})
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	counts := versions(t, pools[0])
	require.Contains(t, counts, int64(1))
	for version, n := range counts {
		require.Equal(t, 1, n, "version %d recorded %d times", version, n)
	}
}

func readyz(t *testing.T, pingPostgres func(context.Context) error) (int, string) {
	t.Helper()
	// Redis is wired in VetMiMi/vetmimi-api#6; until then it is stubbed so
	// these tests see only PostgreSQL's answer.
	redisOK := func(context.Context) error { return nil }
	srv := httptest.NewServer(httpapi.Handler(&httpapi.Server{PingPostgres: pingPostgres, PingRedis: redisOK}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	defer res.Body.Close()
	var body struct {
		Checks struct{ Postgres string }
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	return res.StatusCode, body.Checks.Postgres
}

func TestReadyzWithALivePool(t *testing.T) {
	status, postgres := readyz(t, pgtest.Pool(t).Ping)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", postgres)
}

func TestReadyzWithAClosedPool(t *testing.T) {
	pool, err := platform.OpenPostgres(context.Background(), pgtest.EmptyDatabase(t))
	require.NoError(t, err)
	pool.Close()

	status, postgres := readyz(t, pool.Ping)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "fail", postgres)
}
