package platform_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

func readyzRedis(t *testing.T, rdb *redis.Client) (int, string) {
	t.Helper()
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		PingPostgres: func(context.Context) error { return nil },
		PingRedis:    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		Log:          slog.New(slog.DiscardHandler),
	}))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/readyz")
	require.NoError(t, err)
	defer res.Body.Close()
	var body struct {
		Checks struct{ Redis string }
	}
	require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
	return res.StatusCode, body.Checks.Redis
}

func TestReadyzWithLiveRedis(t *testing.T) {
	status, check := readyzRedis(t, platform.RedisForTest(t))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", check)
}

func TestReadyzWithRedisStopped(t *testing.T) {
	rdb, err := platform.OpenRedis(platform.UnusedRedisURL)
	require.NoError(t, err)
	defer rdb.Close()

	status, check := readyzRedis(t, rdb)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "fail", check)
}
