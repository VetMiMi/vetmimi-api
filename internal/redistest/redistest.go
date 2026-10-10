// Package redistest gives tests a Redis client: a live one on REDIS_URL_TEST,
// or one on a port nothing listens on. Tests share the database, so each
// deletes only its own keys and never flushes it.
package redistest

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
)

// Client returns a client on REDIS_URL_TEST. It fails rather than skips
// without one: a skipped test reads as a pass.
func Client(t testing.TB) *redis.Client {
	t.Helper()
	url := os.Getenv("REDIS_URL_TEST")
	if url == "" {
		t.Fatal("REDIS_URL_TEST is not set; point it at a Redis database tests may write to, " +
			"for example redis://localhost:6379/1")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal("redistest: REDIS_URL_TEST is not a valid redis:// URL")
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal("redistest: ", err)
	}
	return rdb
}

// Stopped returns a client on a port nothing listens on, so every command is
// refused.
func Stopped(t testing.TB) *redis.Client {
	t.Helper()
	opts, err := redis.ParseURL("redis://127.0.0.1:1/0")
	if err != nil {
		t.Fatal("redistest: ", err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	return rdb
}
