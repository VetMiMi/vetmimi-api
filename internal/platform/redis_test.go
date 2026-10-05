package platform

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi"
)

// unusedRedis is a port nothing listens on, so every command is refused.
const unusedRedis = "redis://127.0.0.1:1/0"

// testRedis returns a client on REDIS_URL_TEST. It fails rather than skips
// without one: a skipped test reads as a pass.
func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL_TEST")
	if url == "" {
		t.Fatal("REDIS_URL_TEST is not set; point it at a Redis database tests may write to, " +
			"for example redis://localhost:6379/1")
	}
	rdb, err := OpenRedis(url)
	require.NoError(t, err)
	t.Cleanup(func() { rdb.Close() })
	require.NoError(t, rdb.Ping(context.Background()).Err())
	return rdb
}

// testNamespace returns a queue-name prefix of the test's own and deletes
// every key under it when the test ends. CI tests two packages at once
// against one Redis database, so tests never flush it.
func testNamespace(t *testing.T, rdb *redis.Client) string {
	t.Helper()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	ns := "test-" + hex.EncodeToString(random) + "-"

	t.Cleanup(func() {
		ctx := context.Background()
		var keys []string
		iter := rdb.Scan(ctx, 0, "asynq:{"+ns+"*", 100).Iterator()
		for iter.Next(ctx) {
			keys = append(keys, iter.Val())
		}
		require.NoError(t, iter.Err())
		if len(keys) > 0 {
			require.NoError(t, rdb.Del(ctx, keys...).Err())
		}
		require.NoError(t, rdb.SRem(ctx, "asynq:queues", ns+QueueCritical, ns+QueueDefault, ns+QueueLow).Err())
	})
	return ns
}

// logBuffer collects JSON log lines from goroutines asynq starts.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// entries returns the decoded lines whose message is msg. It may run in a
// require.Eventually goroutine, where a test cannot fail, so it panics on a
// line slog could not have written.
func (b *logBuffer) entries(msg string) []map[string]any {
	var found []map[string]any
	for line := range strings.Lines(b.String()) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			panic(err)
		}
		if entry["msg"] == msg {
			found = append(found, entry)
		}
	}
	return found
}

func TestOpenRedisDoesNotQuoteTheURL(t *testing.T) {
	_, err := OpenRedis("redis://:hunter2-secret@127.0.0.1:notaport/0")
	require.Error(t, err)
	require.NotContains(t, err.Error(), "hunter2-secret")
}

func readyzRedis(t *testing.T, rdb *redis.Client) (int, string) {
	t.Helper()
	srv := httptest.NewServer(httpapi.Handler(&httpapi.Server{
		PingPostgres: func(context.Context) error { return nil },
		PingRedis:    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
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
	status, check := readyzRedis(t, testRedis(t))
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "ok", check)
}

func TestReadyzWithRedisStopped(t *testing.T) {
	rdb, err := OpenRedis(unusedRedis)
	require.NoError(t, err)
	defer rdb.Close()

	status, check := readyzRedis(t, rdb)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "fail", check)
}
