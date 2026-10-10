package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/postgres"
	"github.com/VetMiMi/vetmimi-api/internal/redistest"
)

func get(t *testing.T, h http.Handler, path string) (*http.Response, map[string]any) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()

	res, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer res.Body.Close()

	var body map[string]any
	if res.Header.Get("Content-Type") == "application/json" {
		require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	}
	return res, body
}

func ok(context.Context) error { return nil }

// quiet drops the request log for tests that are not about it.
var quiet = slog.New(slog.DiscardHandler)

func TestHealthz(t *testing.T) {
	res, body := get(t, NewRouter(Deps{Log: quiet}), "/healthz")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "ok", body["status"])
}

func TestReadyzWithBothDependencies(t *testing.T) {
	res, body := get(t, NewRouter(Deps{PingPostgres: ok, PingRedis: ok, Log: quiet}), "/readyz")
	require.Equal(t, http.StatusOK, res.StatusCode)
	require.Equal(t, "ok", body["status"])
}

func TestReadyzReportsAFailingDependency(t *testing.T) {
	down := func(context.Context) error { return errors.New("connection refused") }
	res, body := get(t, NewRouter(Deps{PingPostgres: ok, PingRedis: down, Log: quiet}), "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
	require.Equal(t, "unavailable", body["status"])
	checks := body["checks"].(map[string]any)
	require.Equal(t, "ok", checks["postgres"])
	require.Equal(t, "fail", checks["redis"])
}

func TestReadyzIsUnavailableBeforeWiring(t *testing.T) {
	res, _ := get(t, NewRouter(Deps{Log: quiet}), "/readyz")
	require.Equal(t, http.StatusServiceUnavailable, res.StatusCode)
}

// readyzChecks asks /readyz with real pings and returns its status and checks.
func readyzChecks(t *testing.T, pingPostgres, pingRedis func(context.Context) error) (int, map[string]any) {
	t.Helper()
	res, body := get(t, NewRouter(Deps{PingPostgres: pingPostgres, PingRedis: pingRedis, Log: quiet}), "/readyz")
	return res.StatusCode, body["checks"].(map[string]any)
}

func TestReadyzWithLiveRedis(t *testing.T) {
	rdb := redistest.Client(t)
	status, checks := readyzChecks(t, ok, func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", checks["redis"])
}

func TestReadyzWithRedisStopped(t *testing.T) {
	rdb := redistest.Stopped(t)
	status, checks := readyzChecks(t, ok, func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "fail", checks["redis"])
}

func TestReadyzWithALivePool(t *testing.T) {
	status, checks := readyzChecks(t, pgtest.Pool(t).Ping, ok)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", checks["postgres"])
}

func TestReadyzWithAClosedPool(t *testing.T) {
	pool, err := postgres.Open(context.Background(), pgtest.EmptyDatabase(t))
	require.NoError(t, err)
	pool.Close()

	status, checks := readyzChecks(t, pool.Ping, ok)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "fail", checks["postgres"])
}
