package ratelimit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/redistest"
)

// fakeClock is a clock a test moves by hand.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

// testLimiter returns a Limiter on REDIS_URL_TEST whose keys start with a
// prefix of the test's own, the prefix, and the clock it reads.
func testLimiter(t *testing.T) (*Limiter, string, *fakeClock) {
	t.Helper()
	rdb := redistest.Client(t)
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	prefix := "test-" + hex.EncodeToString(random) + ":"
	t.Cleanup(func() {
		if keys := keysUnder(t, rdb, prefix); len(keys) > 0 {
			require.NoError(t, rdb.Del(context.Background(), keys...).Err())
		}
	})
	clock := &fakeClock{at: time.Date(2026, 10, 5, 9, 30, 15, 0, time.UTC)}
	return New(rdb, prefix, clock.now), prefix, clock
}

func keysUnder(t *testing.T, rdb *redis.Client, prefix string) []string {
	t.Helper()
	ctx := context.Background()
	var keys []string
	iter := rdb.Scan(ctx, 0, prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	require.NoError(t, iter.Err())
	return keys
}

func allow(t *testing.T, l *Limiter, subject string, limit int, window time.Duration) (bool, time.Duration) {
	t.Helper()
	allowed, retryAfter, err := l.Allow(context.Background(), "test", subject, limit, window)
	require.NoError(t, err)
	return allowed, retryAfter
}

func TestAllowCountsUpToTheLimit(t *testing.T) {
	l, _, _ := testLimiter(t)
	for i := range 3 {
		allowed, retryAfter := allow(t, l, "203.0.113.9", 3, time.Minute)
		require.True(t, allowed, "request %d", i+1)
		require.Zero(t, retryAfter)
	}
	allowed, retryAfter := allow(t, l, "203.0.113.9", 3, time.Minute)
	require.False(t, allowed)
	require.Equal(t, 45*time.Second, retryAfter, "09:30:15 waits for the window starting 09:31:00")
}

func TestSubjectsAndGroupsAreCountedApart(t *testing.T) {
	l, _, _ := testLimiter(t)
	allowed, _ := allow(t, l, "203.0.113.9", 1, time.Minute)
	require.True(t, allowed)

	allowed, _ = allow(t, l, "203.0.113.10", 1, time.Minute)
	require.True(t, allowed, "another subject has its own counter")
	allowed, _, err := l.Allow(context.Background(), "other", "203.0.113.9", 1, time.Minute)
	require.NoError(t, err)
	require.True(t, allowed, "another group has its own counter")

	allowed, _ = allow(t, l, "203.0.113.9", 1, time.Minute)
	require.False(t, allowed)
}

func TestCounterStartsAgainInTheNextWindow(t *testing.T) {
	l, _, clock := testLimiter(t)
	clock.at = time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	for range 5 {
		allowed, _ := allow(t, l, "203.0.113.9", 5, 10*time.Minute)
		require.True(t, allowed)
	}

	clock.at = clock.at.Add(10*time.Minute - time.Nanosecond)
	allowed, retryAfter := allow(t, l, "203.0.113.9", 5, 10*time.Minute)
	require.False(t, allowed, "09:09:59.999 is still the 09:00 window")
	require.Equal(t, time.Nanosecond, retryAfter)

	clock.at = clock.at.Add(time.Nanosecond)
	allowed, _ = allow(t, l, "203.0.113.9", 5, 10*time.Minute)
	require.True(t, allowed, "09:10 starts a new window")
}

func TestKeyHoldsAHashOfTheSubjectAndExpiresWithItsWindow(t *testing.T) {
	l, prefix, clock := testLimiter(t)
	const subject = "zz-secret-token-zz"
	allow(t, l, subject, 5, time.Hour)

	keys := keysUnder(t, l.rdb, prefix)
	want := fmt.Sprintf("%srl:test:%s:%d", prefix, hashSubject(subject), clock.at.Truncate(time.Hour).Unix())
	require.Equal(t, []string{want}, keys)
	require.Regexp(t, `^[0-9a-f]{32}$`, hashSubject(subject))
	require.NotContains(t, keys[0], subject)

	ttl, err := l.rdb.TTL(context.Background(), want).Result()
	require.NoError(t, err)
	require.Positive(t, ttl, "a counter without an expiry would never go")
	require.LessOrEqual(t, ttl, time.Hour)
}

// A stopped Redis must cost a limited request a bounded wait, not go-redis's
// whole retry schedule.
func TestAllowWithRedisStoppedFailsWithinItsTimeout(t *testing.T) {
	l := New(redistest.Stopped(t), "", time.Now)

	began := time.Now()
	allowed, _, err := l.Allow(context.Background(), "test", "203.0.113.9", 5, time.Minute)
	elapsed := time.Since(began)

	require.Error(t, err)
	require.False(t, allowed)
	t.Logf("Allow against a stopped Redis returned after %s", elapsed)
	require.Less(t, elapsed, timeout+150*time.Millisecond)
}
