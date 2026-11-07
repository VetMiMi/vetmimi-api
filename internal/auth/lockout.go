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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
)

// Lockout limits sign-in attempts per email, known or not, so its answers
// reveal nothing. Its Redis keys hold a hash of the email and expire.
type Lockout struct {
	limiter *ratelimit.Limiter
	rdb     *redis.Client
	prefix  string // starts every key; tests use their own
	now     clock.Now
}

// Per email: ten attempts an hour, and ten failures inside 15 minutes lock
// the email until 15 minutes after the tenth.
const (
	attemptsPerHour = 10
	lockoutFailures = 10
	failureWindow   = 15 * time.Minute
	lockoutPeriod   = 15 * time.Minute
)

// lockoutTimeout makes a stopped Redis cost a sign-in a short wait, not
// go-redis's whole retry schedule.
const lockoutTimeout = 250 * time.Millisecond

var errNoLockout = errors.New("no sign-in lockout")

func NewLockout(rdb *redis.Client, prefix string, now clock.Now) *Lockout {
	return &Lockout{
		limiter: ratelimit.New(rdb, prefix, now),
		rdb:     rdb,
		prefix:  prefix,
		now:     now,
	}
}

// EmailHashPrefix tells one email's attempts from another's in a log line,
// without the email.
func EmailHashPrefix(email string) string {
	return emailHash(email)[:12]
}

func emailHash(email string) string {
	sum := sha256.Sum256([]byte(NormalizeEmail(email)))
	return hex.EncodeToString(sum[:16])
}

func (lockout *Lockout) key(kind, email string) string {
	return fmt.Sprintf("%slockout:%s:%s", lockout.prefix, kind, emailHash(email))
}

// admit counts one attempt and refuses it with rate_limited while the email
// is over its hourly limit or locked out. It runs before any password is
// hashed, and fails closed with unavailable when Redis does not answer.
func (lockout *Lockout) admit(ctx context.Context, email string) error {
	if lockout == nil {
		return signInUnavailable(errNoLockout)
	}
	allowed, wait, err := lockout.limiter.Allow(ctx, "sign-in-email", NormalizeEmail(email), attemptsPerHour, time.Hour)
	if err != nil {
		return signInUnavailable(err)
	}
	until, err := lockout.lockedUntil(ctx, email)
	if err != nil {
		return signInUnavailable(err)
	}
	now := lockout.now()
	if !allowed || now.Before(until) {
		return apperr.RateLimit(max(wait, until.Sub(now)))
	}
	return nil
}

// lockedUntil returns the zero time when email is not locked out.
func (lockout *Lockout) lockedUntil(ctx context.Context, email string) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, lockoutTimeout)
	defer cancel()
	milliseconds, err := lockout.rdb.Get(ctx, lockout.key("until", email)).Int64()
	if errors.Is(err, redis.Nil) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("sign-in lockout: %w", err)
	}
	return time.UnixMilli(milliseconds), nil
}

// failed records a refused sign-in and locks the email on the
// lockoutFailures-th failure inside failureWindow.
func (lockout *Lockout) failed(ctx context.Context, email string) error {
	if lockout == nil {
		return signInUnavailable(errNoLockout)
	}
	ctx, cancel := context.WithTimeout(ctx, lockoutTimeout)
	defer cancel()
	now := lockout.now()
	count, err := lockout.addFailure(ctx, email, now)
	if err != nil {
		return signInUnavailable(fmt.Errorf("sign-in lockout: %w", err))
	}
	if count < lockoutFailures {
		return nil
	}

	until := now.Add(lockoutPeriod)
	err = lockout.rdb.Set(ctx, lockout.key("until", email), until.UnixMilli(), lockoutPeriod).Err()
	if err != nil {
		return signInUnavailable(fmt.Errorf("sign-in lockout: %w", err))
	}
	return nil
}

// addFailure returns the failures inside failureWindow, counting this one.
func (lockout *Lockout) addFailure(ctx context.Context, email string, now time.Time) (int64, error) {
	key := lockout.key("failures", email)
	var count *redis.IntCmd
	// One transaction, so the set is never left without its expiry.
	_, err := lockout.rdb.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.ZRemRangeByScore(ctx, key, "-inf", strconv.FormatInt(now.Add(-failureWindow).UnixMilli(), 10))
		// A random member, so two failures at the same instant both count.
		pipe.ZAdd(ctx, key, redis.Z{Score: float64(now.UnixMilli()), Member: rand.Text()})
		count = pipe.ZCard(ctx, key)
		pipe.PExpire(ctx, key, failureWindow)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count.Val(), nil
}

// signInUnavailable wraps the cause so the error log names it.
func signInUnavailable(cause error) error {
	return fmt.Errorf("%w: %w", apperr.New(apperr.Unavailable, "Sign-in is unavailable; try again shortly."), cause)
}
