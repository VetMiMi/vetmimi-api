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
