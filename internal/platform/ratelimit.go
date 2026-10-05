package platform

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// limiterTimeout bounds one Allow. go-redis retries a refused connection with
// backoff for about 1.7 seconds, and every limited request asks the limiter
// first, so without a bound a stopped Redis would stall every request that
// long.
const limiterTimeout = 250 * time.Millisecond

// Limiter counts requests in fixed windows in Redis. A window starts on a
// multiple of its length, so every subject's minute starts on the minute.
type Limiter struct {
	rdb *redis.Client
	now clock.Now
	// prefix starts every key. Production passes ""; tests pass one of their
	// own, so test binaries sharing one Redis never count each other's
	// requests and each deletes only its own keys.
	prefix string
}

// NewLimiter returns a Limiter that keeps its counters in rdb and reads the
// time from now.
func NewLimiter(rdb *redis.Client, prefix string, now clock.Now) *Limiter {
	return &Limiter{rdb: rdb, now: now, prefix: prefix}
}

// Allow counts one request by subject in group and reports whether it is
// within limit for the current window; if not, retryAfter is the time until
// the next window starts. Redis keeps only a hash of subject, so the IPs,
// tokens and email addresses limits are keyed by are never stored there. An
// error means Redis did not answer within limiterTimeout; the caller decides
// whether to let the request through.
func (l *Limiter) Allow(ctx context.Context, group, subject string, limit int, window time.Duration) (allowed bool, retryAfter time.Duration, err error) {
	now := l.now()
	start := now.Truncate(window)
	key := fmt.Sprintf("%srl:%s:%s:%d", l.prefix, group, hashSubject(subject), start.Unix())

	ctx, cancel := context.WithTimeout(ctx, limiterTimeout)
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

// hashSubject keeps the first 32 hex characters of the subject's SHA-256:
// 128 bits are ample to keep subjects apart, and a key listing shows no
// address, token or email as written.
func hashSubject(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:16])
}
