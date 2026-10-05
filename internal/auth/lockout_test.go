package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// Failures with a wrong password hash with argon2 like any other sign-in, so
// only the tests about hashing or about the right password use one; the
// others fail with a wrong code and AcceptEveryPassword.

// testRedis is REDIS_URL_TEST with a key prefix of the test's own, whose keys
// are deleted when the test ends; the database is never flushed.
type testRedis struct {
	client *redis.Client
	prefix string
}

func newTestRedis(t *testing.T) *testRedis {
	t.Helper()
	url := os.Getenv("REDIS_URL_TEST")
	if url == "" {
		t.Fatal("REDIS_URL_TEST is not set; point it at a Redis database tests may write to, " +
			"for example redis://localhost:6379/1")
	}
	client, err := platform.OpenRedis(url)
	require.NoError(t, err)
	t.Cleanup(func() { client.Close() })
	random := make([]byte, 6)
	_, err = rand.Read(random)
	require.NoError(t, err)
	r := &testRedis{client: client, prefix: "test-" + hex.EncodeToString(random) + ":"}
	t.Cleanup(func() {
		if keys := r.keys(t); len(keys) > 0 {
			require.NoError(t, client.Del(context.Background(), keys...).Err())
		}
	})
	return r
}

func (r *testRedis) keys(t *testing.T) []string {
	t.Helper()
	ctx := context.Background()
	var keys []string
	iter := r.client.Scan(ctx, 0, r.prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	require.NoError(t, iter.Err())
	return keys
}

// at is a time on the fixture's day, in UTC.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 10, 5, hour, minute, second, 0, time.UTC)
}

// wrongCode is a well-formed code that matches none of the steps accepted at
// the fixture's clock time.
func (f *fixture) wrongCode(t *testing.T, secret []byte) string {
	t.Helper()
	accepted := map[string]bool{}
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		accepted[codeAt(t, secret, f.clock.at.Add(offset))] = true
	}
	n := 0
	for accepted[fmt.Sprintf("%06d", n)] {
		n++
	}
	return fmt.Sprintf("%06d", n)
}

// failWrongCode signs in with a wrong code n times; with AcceptEveryPassword
// in force, no password is hashed.
func (f *fixture) failWrongCode(t *testing.T, a admin, n int) {
	t.Helper()
	for range n {
		_, err := f.signIn(t, a.email, password, f.wrongCode(t, a.secret))
		requireInvalidCredentials(t, err)
	}
}

// failWrongPassword signs in as email with a wrong password n times.
func (f *fixture) failWrongPassword(t *testing.T, email string, secret []byte, n int) {
	t.Helper()
	for range n {
		_, err := f.signIn(t, email, password+"!", codeAt(t, secret, f.clock.at))
		requireInvalidCredentials(t, err)
	}
}

func (f *fixture) signInRight(t *testing.T, a admin) (auth.SignedIn, error) {
	t.Helper()
	return f.signIn(t, a.email, password, codeAt(t, a.secret, f.clock.at))
}

func requireRateLimited(t *testing.T, err error, wait time.Duration) {
	t.Helper()
	var appErr *apperr.Error
	require.True(t, errors.As(err, &appErr), "got %v", err)
	require.Equal(t, apperr.RateLimited, appErr.Code)
	require.Equal(t, wait, appErr.RetryAfter)
}

func requireUnavailable(t *testing.T, err error) {
	t.Helper()
	var appErr *apperr.Error
	require.True(t, errors.As(err, &appErr), "got %v", err)
	require.Equal(t, apperr.Unavailable, appErr.Code)
}

// Ten failures straddle the hour, so the hourly limit (five in each hour) is
// not what refuses the eleventh attempt: only the lockout can.
func TestLockoutAfterTenFailures(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	unknown := uniqueEmail(t)

	for name, email := range map[string]string{"existing email": a.email, "unknown email": unknown} {
		t.Run(name, func(t *testing.T) {
			checked := auth.RecordPasswordChecks(t)
			f.clock.at = at(9, 50, 0)
			f.failWrongPassword(t, email, a.secret, 5)
			f.clock.at = at(10, 0, 0)
			f.failWrongPassword(t, email, a.secret, 5)
			require.Len(t, *checked, 10)

			_, err := f.signIn(t, email, password, codeAt(t, a.secret, f.clock.at))
			requireRateLimited(t, err, 15*time.Minute)
			require.Len(t, *checked, 10, "a locked email costs no hashing, even with the right password")

			f.clock.at = at(10, 14, 59)
			_, err = f.signIn(t, email, password, codeAt(t, a.secret, f.clock.at))
			requireRateLimited(t, err, time.Second)
			failures, err := f.redis.client.ZCard(context.Background(), auth.FailuresKey(f.redis.prefix, email)).Result()
			require.NoError(t, err)
			require.EqualValues(t, 10, failures, "a refused attempt is not a failure")

			// The refused attempts did not extend the lockout.
			f.clock.at = at(10, 15, 0)
			_, err = f.signIn(t, email, password, codeAt(t, a.secret, f.clock.at))
			if email == a.email {
				require.NoError(t, err, "fifteen minutes after the tenth failure")
			} else {
				requireInvalidCredentials(t, err)
			}
		})
	}
}

// A failure exactly fifteen minutes old has left the window: nine of those
// and one new failure lock nothing.
func TestFailuresOutsideTheWindowDoNotCount(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	auth.AcceptEveryPassword(t)

	f.clock.at = at(9, 45, 0)
	f.failWrongCode(t, a, 9)
	f.clock.at = at(10, 0, 0)
	f.failWrongCode(t, a, 1)

	_, err := f.signInRight(t, a)
	require.NoError(t, err)
}

// Five failures, then five more twenty minutes later, never lock the email,
// but they are ten attempts in one hour.
func TestPerEmailHourlyLimit(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)

	f.clock.at = at(9, 0, 15)
	f.failWrongPassword(t, a.email, a.secret, 5)
	f.clock.at = at(9, 20, 0)
	f.failWrongPassword(t, a.email, a.secret, 5)

	checked := auth.RecordPasswordChecks(t)
	_, err := f.signInRight(t, a)
	requireRateLimited(t, err, 40*time.Minute)
	require.Empty(t, *checked, "an email over its limit costs no hashing")

	f.clock.at = at(10, 0, 0)
	_, err = f.signInRight(t, a)
	require.NoError(t, err, "a new hour restores access")
}

// The right password after nine failures signs in, but does not wipe the
// nine: one more failure locks the email, so a success cannot tell an
// attacker which of their guesses was right.
func TestSuccessDoesNotResetFailures(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	auth.AcceptEveryPassword(t)

	f.clock.at = at(9, 50, 0)
	f.failWrongCode(t, a, 9)
	f.clock.at = at(10, 0, 0)
	_, err := f.signInRight(t, a)
	require.NoError(t, err)

	f.failWrongCode(t, a, 1)
	_, err = f.signInRight(t, a)
	requireRateLimited(t, err, 15*time.Minute)
}

func TestLockoutKeysHoldAHashOfTheEmailAndExpire(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	auth.AcceptEveryPassword(t)
	f.failWrongCode(t, a, 10)

	keys := f.redis.keys(t)
	require.Len(t, keys, 3, "the hourly count, the failures and the lockout: %v", keys)
	local := strings.Split(a.email, "@")[0]
	for _, key := range keys {
		require.NotContains(t, key, local)
		ttl, err := f.redis.client.PTTL(context.Background(), key).Result()
		require.NoError(t, err)
		require.Positive(t, ttl, "%s would never go", key)
		require.LessOrEqual(t, ttl, time.Hour, key)
	}
	require.Regexp(t, `^[0-9a-f]{12}$`, auth.EmailHashPrefix(a.email))
	require.Equal(t, auth.EmailHashPrefix(a.email), auth.EmailHashPrefix(" "+strings.ToUpper(a.email)))
}

// Sign-in fails closed without Redis, as the HTTP visitor-IP limit does: an
// unlimited sign-in is what an attacker most wants from an outage.
func TestSignInFailsClosedWithoutRedis(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	stopped, err := platform.OpenRedis("redis://127.0.0.1:1/0")
	require.NoError(t, err)
	t.Cleanup(func() { stopped.Close() })

	for name, lockout := range map[string]*auth.Lockout{
		"redis stopped": auth.NewLockout(stopped, "", f.clock.now),
		"no lockout":    nil,
	} {
		sessions, err := auth.NewSessions(pgtest.Pool(t), f.codes, lockout, f.clock.now)
		require.NoError(t, err)
		checked := auth.RecordPasswordChecks(t)
		_, err = sessions.SignIn(context.Background(), auth.Credentials{
			Email: a.email, Password: password, Code: codeAt(t, a.secret, now),
		})
		requireUnavailable(t, err)
		require.Empty(t, *checked, name)
		require.Zero(t, sessionCount(t, a.id), name)
	}
}

// refuse makes Redis refuse one command, alone or in a transaction, and
// nothing else.
type refuse string

var errRefused = errors.New("refused by test")

func (r refuse) DialHook(next redis.DialHook) redis.DialHook { return next }

func (r refuse) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == string(r) {
			cmd.SetErr(errRefused)
			return errRefused
		}
		return next(ctx, cmd)
	}
}

func (r refuse) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		for _, cmd := range cmds {
			if cmd.Name() == string(r) {
				return errRefused
			}
		}
		return next(ctx, cmds)
	}
}

// Each Redis call sign-in makes fails closed on its own: an attempt that
// cannot be counted, checked or recorded is refused, never let through to
// guess on uncounted.
func TestSignInFailsClosedWhenRedisRefuses(t *testing.T) {
	for name, tc := range map[string]struct {
		command      string
		failuresLeft int
	}{
		"counting the attempt": {"incr", 0},
		"reading the lockout":  {"get", 0},
		"recording a failure":  {"zadd", 0},
		"locking the email":    {"set", 9},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			a := f.newAdmin(t)
			auth.AcceptEveryPassword(t)
			f.failWrongCode(t, a, tc.failuresLeft)
			f.redis.client.AddHook(refuse(tc.command))

			_, err := f.signIn(t, a.email, password, f.wrongCode(t, a.secret))
			requireUnavailable(t, err)
			require.ErrorIs(t, err, errRefused)
		})
	}
}
