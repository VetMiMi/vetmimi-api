// Package ratelimit counts requests per subject in fixed windows in Redis.
package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
)

// timeout bounds one Allow: go-redis retries a refused connection for about
// 1.7 seconds, which a stopped Redis would add to every limited request.
const timeout = 250 * time.Millisecond

// Limiter starts each window on a multiple of its length, so every subject's
// minute starts on the minute.
type Limiter struct {
	rdb    *redis.Client
	now    clock.Now
	prefix string // starts every key; "" in production, the test's own in tests
}

func New(rdb *redis.Client, prefix string, now clock.Now) *Limiter {
	return &Limiter{rdb: rdb, now: now, prefix: prefix}
}

// Allow counts one request by subject in group and reports whether it is
// within limit; if not, retryAfter is the time until the next window. Redis
// keeps only a hash of subject. An error means Redis did not answer in time;
// the caller decides whether to let the request through.
func (l *Limiter) Allow(ctx context.Context, group, subject string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error) {
	now := l.now()
	start := now.Truncate(window)
	key := fmt.Sprintf("%srl:%s:%s:%d", l.prefix, group, hashSubject(subject), start.Unix())

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var count *redis.IntCmd
	// One transaction, so a counter is never left without its expiry.
	_, err = l.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		count = p.Incr(ctx, key)
		p.Expire(ctx, key, window)
		return nil
	})
	if err != nil {
		return false, 0, fmt.Errorf("rate limit %s: %w", group, err)
	}
	if count.Val() > int64(limit) {
		return false, start.Add(window).Sub(now), nil
	}
	return true, 0, nil
}

// hashSubject keeps 128 bits of the subject's SHA-256, so a key listing shows
// no address, token or email.
func hashSubject(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:16])
}
