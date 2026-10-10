package auth_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// uniqueEmail keeps tests apart: they share one database.
func uniqueEmail(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "user-" + hex.EncodeToString(b) + "@example.com"
}

type testClock struct{ at time.Time }

func (c *testClock) now() time.Time { return c.at }

// fixture reads codes, expiry and lockouts from one clock the test moves.
type fixture struct {
	clock    *testClock
	codes    *auth.TOTP
	sessions *auth.Sessions
	redis    *testRedis
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := &testClock{at: now}
	codes, err := auth.NewTOTP(testKey, clock.now)
	require.NoError(t, err)
	redis := newTestRedis(t)
	lockout := auth.NewLockout(redis.client, redis.prefix, clock.now)
	sessions, err := auth.NewSessions(pgtest.Pool(t), codes, lockout, clock.now)
	require.NoError(t, err)
	return &fixture{clock: clock, codes: codes, sessions: sessions, redis: redis}
}

// admin is a user whose password is "correct horse battery" (hash) and whose
// codes come from secret.
type admin struct {
	id     pgtype.UUID
	email  string
	secret []byte
}

func (f *fixture) newAdmin(t *testing.T) admin {
	t.Helper()
	a := admin{email: uniqueEmail(t), secret: newSecret(t)}
	sealed, err := f.codes.Seal(a.secret)
	require.NoError(t, err)
	a.id, err = db.New(pgtest.Pool(t)).CreateUser(context.Background(), db.CreateUserParams{
		Email:         a.email,
		DisplayName:   "Daw Mi",
		PasswordHash:  hash,
		Roles:         []string{"booking_admin", "site_admin"},
		TotpSecretEnc: sealed,
	})
	require.NoError(t, err)
	return a
}

func (f *fixture) newPasswordOnlyAdmin(t *testing.T) admin {
	t.Helper()
	a := admin{email: uniqueEmail(t)}
	var err error
	a.id, err = db.New(pgtest.Pool(t)).CreateUser(context.Background(), db.CreateUserParams{
		Email:        a.email,
		DisplayName:  "Daw Mi",
		PasswordHash: hash,
		Roles:        []string{"site_admin"},
	})
	require.NoError(t, err)
	return a
}

// startSession inserts a session for the user as if they had signed in now,
// without hashing a password.
func (f *fixture) startSession(t *testing.T, userID pgtype.UUID) (string, pgtype.UUID) {
	t.Helper()
	token, err := tokens.NewSession()
	require.NoError(t, err)
	id, err := db.New(pgtest.Pool(t)).CreateSession(context.Background(), db.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokens.Hash(token),
		Now:       f.clock.at,
		ExpiresAt: f.clock.at.Add(auth.SessionLifetime),
	})
	require.NoError(t, err)
	return token, id
}

func (f *fixture) authenticate(token string) (auth.Session, error) {
	return f.sessions.Authenticate(context.Background(), token)
}

func requireUnauthenticated(t *testing.T, err error) {
	t.Helper()
	var appErr *apperr.Error
	require.True(t, errors.As(err, &appErr), "got %v", err)
	require.Equal(t, apperr.Unauthenticated, appErr.Code)
}

type sessionRow struct {
	userID              pgtype.UUID
	lastSeen, expiresAt time.Time
	found               bool
}

func loadSession(t *testing.T, id pgtype.UUID) sessionRow {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(),
		"SELECT user_id, last_seen_at, expires_at FROM sessions WHERE id = $1", id)
	require.NoError(t, err)
	defer rows.Close()
	var s sessionRow
	for rows.Next() {
		require.NoError(t, rows.Scan(&s.userID, &s.lastSeen, &s.expiresAt))
		s.found = true
	}
	require.NoError(t, rows.Err())
	return s
}

func TestAuthenticateReturnsTheSessionAndItsUser(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, id := f.startSession(t, a.id)

	got, err := f.authenticate(token)
	require.NoError(t, err)
	require.Equal(t, auth.Session{ID: id, User: auth.User{
		ID:          a.id,
		Email:       a.email,
		DisplayName: "Daw Mi",
		Roles:       []string{"booking_admin", "site_admin"},
		TwoStep:     true,
	}}, got)
}

func TestMalformedOrUnknownTokenIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, _ := f.startSession(t, a.id)
	unknown, err := tokens.NewSession()
	require.NoError(t, err)

	for name, sent := range map[string]string{
		"empty":     "",
		"malformed": "not-a-token",
		"truncated": token[:len(token)-1],
		"too long":  token + "x",
		"unknown":   unknown,
	} {
		_, err := f.authenticate(sent)
		requireUnauthenticated(t, err)
		require.NotContains(t, err.Error(), token[4:], name)
	}
}

// A malformed token is refused before the database is asked: here the pool
// is closed, so any query would fail with another error.
func TestMalformedTokenNeverReachesTheDatabase(t *testing.T) {
	f := newFixture(t)
	closed, err := pgxpool.NewWithConfig(context.Background(), pgtest.Pool(t).Config())
	require.NoError(t, err)
	closed.Close()
	sessions, err := auth.NewSessions(closed, f.codes, nil, f.clock.now)
	require.NoError(t, err)

	for _, sent := range []string{"", "not-a-token", "vms_short"} {
		_, err := sessions.Authenticate(context.Background(), sent)
		requireUnauthenticated(t, err)
	}
	wellFormed, err := tokens.NewSession()
	require.NoError(t, err)
	_, err = sessions.Authenticate(context.Background(), wellFormed)
	require.Error(t, err)
	var appErr *apperr.Error
	require.False(t, errors.As(err, &appErr), "a well-formed token is looked up: %v", err)
}

func TestSessionsTableConstraints(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	pool := pgtest.Pool(t)
	ctx := context.Background()
	token, id := f.startSession(t, a.id)

	_, err := db.New(pool).CreateSession(ctx, db.CreateSessionParams{
		UserID: a.id, TokenHash: tokens.Hash(token), Now: now, ExpiresAt: now.Add(time.Hour),
	})
	requireViolates(t, err, "sessions_token_hash_key")

	_, err = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", a.id)
	require.NoError(t, err)
	require.False(t, loadSession(t, id).found, "deleting a user deletes their sessions")
}

func TestIdleSessionExpires(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	idle, _ := f.startSession(t, a.id)
	active, _ := f.startSession(t, a.id)

	f.clock.at = now.Add(auth.SessionIdle - time.Second)
	_, err := f.authenticate(active)
	require.NoError(t, err, "a second short of twelve idle hours")

	f.clock.at = now.Add(auth.SessionIdle + time.Second)
	_, err = f.authenticate(idle)
	requireUnauthenticated(t, err)
	_, err = f.authenticate(active)
	require.NoError(t, err, "its last request was two seconds ago")
}

func TestAbsoluteExpiry(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, _ := f.startSession(t, a.id)

	// Used every hour for a week, so it is never idle.
	for f.clock.at = now; f.clock.at.Before(now.Add(auth.SessionLifetime)); f.clock.at = f.clock.at.Add(time.Hour) {
		_, err := f.authenticate(token)
		require.NoError(t, err, f.clock.at)
	}
	f.clock.at = now.Add(auth.SessionLifetime - time.Second)
	_, err := f.authenticate(token)
	require.NoError(t, err)

	f.clock.at = now.Add(auth.SessionLifetime)
	_, err = f.authenticate(token)
	requireUnauthenticated(t, err)
}

func TestDisabledUsersSessionIsRefused(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, _ := f.startSession(t, a.id)
	_, err := f.authenticate(token)
	require.NoError(t, err)

	_, err = pgtest.Pool(t).Exec(context.Background(), "UPDATE users SET disabled_at = now() WHERE id = $1", a.id)
	require.NoError(t, err)
	_, err = f.authenticate(token)
	requireUnauthenticated(t, err)
}

// last_seen_at is written at most once a minute, not on every request.
func TestLastSeenIsRefreshedOnlyWhenOverAMinuteOld(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, id := f.startSession(t, a.id)

	for _, after := range []time.Duration{5 * time.Second, 10 * time.Second, time.Minute} {
		f.clock.at = now.Add(after)
		_, err := f.authenticate(token)
		require.NoError(t, err)
		require.Equal(t, now, loadSession(t, id).lastSeen.UTC(), "%s after sign-in", after)
	}

	f.clock.at = now.Add(61 * time.Second)
	_, err := f.authenticate(token)
	require.NoError(t, err)
	require.Equal(t, f.clock.at, loadSession(t, id).lastSeen.UTC())

	f.clock.at = now.Add(65 * time.Second)
	_, err = f.authenticate(token)
	require.NoError(t, err)
	require.Equal(t, now.Add(61*time.Second), loadSession(t, id).lastSeen.UTC())
}

func TestSignOutEndsTheSessionAtOnce(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	token, id := f.startSession(t, a.id)
	other, _ := f.startSession(t, a.id)

	require.NoError(t, f.sessions.SignOut(context.Background(), id))
	require.False(t, loadSession(t, id).found)
	_, err := f.authenticate(token)
	requireUnauthenticated(t, err)
	_, err = f.authenticate(other)
	require.NoError(t, err, "signing out ends this session only")
}

func TestCleanupDeletesOnlyExpiredSessions(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	pool := pgtest.Pool(t)
	ctx := context.Background()

	_, live := f.startSession(t, a.id)
	_, idle := f.startSession(t, a.id)
	_, old := f.startSession(t, a.id)
	cleanupAt := now.Add(auth.SessionLifetime)
	// live was used a minute ago; idle twelve hours ago; old was used a minute
	// ago too, but signed in seven days ago.
	_, err := pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE id = $1",
		live, cleanupAt.Add(-time.Minute), cleanupAt.Add(time.Hour))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $2, expires_at = $3 WHERE id = $1",
		idle, cleanupAt.Add(-auth.SessionIdle), cleanupAt.Add(time.Hour))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "UPDATE sessions SET last_seen_at = $2 WHERE id = $1", old, cleanupAt.Add(-time.Minute))
	require.NoError(t, err)

	deleted, err := auth.DeleteExpiredSessions(ctx, db.New(pool), cleanupAt)
	require.NoError(t, err)
	require.GreaterOrEqual(t, deleted, int64(2))
	require.True(t, loadSession(t, live).found)
	require.False(t, loadSession(t, idle).found)
	require.False(t, loadSession(t, old).found)

	again, err := auth.DeleteExpiredSessions(ctx, db.New(pool), cleanupAt)
	require.NoError(t, err)
	require.Zero(t, again, "deleting twice deletes nothing")
}

// failSessionWrites makes PostgreSQL refuse op ("INSERT" or "DELETE") on the
// user's sessions until the test ends. The trigger names only this user, so
// other tests sharing the database are unaffected.
func failSessionWrites(t *testing.T, op string, userID pgtype.UUID) {
	t.Helper()
	pool := pgtest.Pool(t)
	ctx := context.Background()
	random := make([]byte, 6)
	_, err := rand.Read(random)
	require.NoError(t, err)
	name := "refuse_" + hex.EncodeToString(random)
	row := "NEW"
	if op == "DELETE" {
		row = "OLD"
	}
	_, err = pool.Exec(ctx, `CREATE FUNCTION `+name+`() RETURNS trigger LANGUAGE plpgsql AS
		$$ BEGIN RAISE EXCEPTION 'refused by test'; END $$`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `CREATE TRIGGER `+name+` BEFORE `+op+` ON sessions FOR EACH ROW
		WHEN (`+row+`.user_id = '`+userID.String()+`') EXECUTE FUNCTION `+name+`()`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, `DROP TRIGGER `+name+` ON sessions; DROP FUNCTION `+name+`()`)
		require.NoError(t, err)
	})
}

func sessionCount(t *testing.T, userID pgtype.UUID) int {
	t.Helper()
	var n int
	err := pgtest.Pool(t).QueryRow(context.Background(), "SELECT count(*) FROM sessions WHERE user_id = $1", userID).Scan(&n)
	require.NoError(t, err)
	return n
}

func TestSessionTravelsInTheContext(t *testing.T) {
	_, ok := auth.FromContext(context.Background())
	require.False(t, ok)

	s := auth.Session{User: auth.User{Email: "mi@example.com", Roles: []string{"booking_admin"}}}
	got, ok := auth.FromContext(auth.WithSession(context.Background(), s))
	require.True(t, ok)
	require.Equal(t, s, got)
}
