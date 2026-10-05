package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// The per-email sign-in limits in docs/architecture.md: ten attempts an
// hour, and ten failures inside 15 minutes lock the email until 15 minutes
// after the tenth. The visitor-IP limit is the HTTP layer's.
const (
	attemptsPerHour = 10
	lockoutFailures = 10
	failureWindow   = 15 * time.Minute
	lockoutPeriod   = 15 * time.Minute
)

// lockoutTimeout bounds the Redis calls of one sign-in, as platform.Limiter
// bounds its own: a stopped Redis must cost a sign-in a short wait, not
// go-redis's whole retry schedule.
const lockoutTimeout = 250 * time.Millisecond

// errNoLockout stands for Redis when Sessions was built without a Lockout,
// so it refuses every sign-in rather than allowing unlimited guesses.
var errNoLockout = errors.New("no sign-in lockout")

// Lockout limits sign-in attempts for each email, whether or not it has an
// account, so its answers reveal nothing about which emails do. Its Redis
// keys hold a hash of the lower-cased email, never the email, and expire on
// their own. Times come from its clock, not Redis's, and are stored as
// values, so a test can move past a window without waiting for it.
type Lockout struct {
	limiter *platform.Limiter
	rdb     *redis.Client
	// prefix starts every key: "" in production, a test's own in tests, as
	// for platform.Limiter.
	prefix string
	now    clock.Now
}

// NewLockout returns a Lockout keeping its counters in rdb under prefix and
// reading the time from now.
func NewLockout(rdb *redis.Client, prefix string, now clock.Now) *Lockout {
	return &Lockout{limiter: platform.NewLimiter(rdb, prefix, now), rdb: rdb, prefix: prefix, now: now}
}

// EmailHashPrefix is the first 12 hex characters of the SHA-256 of the
// lower-cased email: enough for a log line to tell one email's attempts from
// another's, without the email.
func EmailHashPrefix(email string) string { return emailHash(email)[:12] }

// emailHash keys an email's counters: the first 32 hex characters of its
// SHA-256, as platform.Limiter keys its subjects.
func emailHash(email string) string {
	sum := sha256.Sum256([]byte(NormalizeEmail(email)))
	return hex.EncodeToString(sum[:16])
}

func (l *Lockout) key(kind, email string) string {
	return fmt.Sprintf("%slockout:%s:%s", l.prefix, kind, emailHash(email))
}

// admit counts one attempt for email against its hourly limit, and refuses
// it with rate_limited while the email is over that limit or locked out,
// telling the client to wait until both have passed. It runs before any
// password is hashed, so a refused attempt costs no argon2 work. If Redis
// does not answer it refuses with unavailable: sign-in fails closed, as the
// HTTP visitor-IP limit does.
func (l *Lockout) admit(ctx context.Context, email string) error {
	if l == nil {
		return signInUnavailable(errNoLockout)
	}
	allowed, wait, err := l.limiter.Allow(ctx, "sign-in-email", NormalizeEmail(email), attemptsPerHour, time.Hour)
	if err != nil {
		return signInUnavailable(err)
	}
	until, err := l.lockedUntil(ctx, email)
	if err != nil {
		return signInUnavailable(err)
	}
	now := l.now()
	if !allowed || now.Before(until) {
		return apperr.RateLimit(max(wait, until.Sub(now)))
	}
	return nil
}

// lockedUntil is when email's lockout ends, or the zero time if it has none.
func (l *Lockout) lockedUntil(ctx context.Context, email string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, lockoutTimeout)
	defer cancel()
	ms, err := l.rdb.Get(ctx, l.key("until", email)).Int64()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("sign-in lockout: %w", err)
	}
	return time.UnixMilli(ms), nil
}

// failed records a refused sign-in for email and, when it is the
// lockoutFailures-th inside failureWindow, locks the email for
// lockoutPeriod. Failures that have left the window are dropped first, so
// they never count. Each failure is a member of its own, so two at the same
// instant are both counted.
func (l *Lockout) failed(ctx context.Context, email string) error {
	if l == nil {
		return signInUnavailable(errNoLockout)
	}
	ctx, cancel := context.WithTimeout(ctx, lockoutTimeout)
	defer cancel()
	now := l.now()
	key := l.key("failures", email)
	var count *redis.IntCmd
	// One transaction, so the set is never left without its expiry.
	_, err := l.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-failureWindow).UnixMilli(), 10))
		p.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMilli()), Member: rand.Text()})
		count = p.ZCard(ctx, key)
		p.PExpire(ctx, key, failureWindow)
		return nil
	})
	if err != nil {
		return signInUnavailable(fmt.Errorf("sign-in lockout: %w", err))
	}
	if count.Val() < lockoutFailures {
		return nil
	}
	until := now.Add(lockoutPeriod)
	if err := l.rdb.Set(ctx, l.key("until", email), until.UnixMilli(), lockoutPeriod).Err(); err != nil {
		return signInUnavailable(fmt.Errorf("sign-in lockout: %w", err))
	}
	return nil
}

// signInUnavailable is the 503 the HTTP limit answers when Redis is down,
// wrapping the cause so the error log names it.
func signInUnavailable(cause error) error {
	return fmt.Errorf("%w: %w", apperr.New(apperr.Unavailable, "Sign-in is unavailable; try again shortly."), cause)
}
