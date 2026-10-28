package main

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/postgres"
	"github.com/VetMiMi/vetmimi-api/internal/redistest"
	"github.com/VetMiMi/vetmimi-api/internal/web"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func healthAPI(pingPostgres, pingRedis func(context.Context) error) *webtest.API {
	r := web.NewRouter()
	r.GET("/healthz", healthz)
	r.GET("/readyz", readyz(pingPostgres, pingRedis))
	return &webtest.API{Router: r}
}

func TestHTTP_Healthz(t *testing.T) {
	down := func(context.Context) error { return context.DeadlineExceeded }
	a := healthAPI(down, down)
	require.Equal(t, map[string]any{"status": "ok"}, webtest.JSON(t, http.StatusOK, a.Send(http.MethodGet, "/healthz", "")),
		"liveness touches no dependency")
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Send(http.MethodPost, "/healthz", ""))
}

func TestHTTP_ReadyzWithLiveDependencies(t *testing.T) {
	rdb := redistest.Client(t)
	a := healthAPI(pgtest.Pool(t).Ping, func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
	require.Equal(t, map[string]any{"status": "ok", "checks": map[string]any{"postgres": "ok", "redis": "ok"}},
		webtest.JSON(t, http.StatusOK, a.Send(http.MethodGet, "/readyz", "")))
}

func TestHTTP_ReadyzWithADependencyDown(t *testing.T) {
	rdb := redistest.Stopped(t)
	a := healthAPI(pgtest.Pool(t).Ping, func(ctx context.Context) error { return rdb.Ping(ctx).Err() })
	require.Equal(t, map[string]any{"status": "unavailable", "checks": map[string]any{"postgres": "ok", "redis": "fail"}},
		webtest.JSON(t, http.StatusServiceUnavailable, a.Send(http.MethodGet, "/readyz", "")))

	pool, err := postgres.Open(context.Background(), pgtest.EmptyDatabase(t))
	require.NoError(t, err)
	pool.Close()
	a = healthAPI(pool.Ping, func(context.Context) error { return nil })
	got := webtest.JSON(t, http.StatusServiceUnavailable, a.Send(http.MethodGet, "/readyz", ""))
	require.Equal(t, map[string]any{"postgres": "fail", "redis": "ok"}, got["checks"])
}
