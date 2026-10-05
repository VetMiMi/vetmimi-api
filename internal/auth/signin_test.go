package auth_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// Every sign-in hashes a password with 64 MiB of memory, so these tests sign
// in as few times as they can, and never more than two at once.

const password = "correct horse battery"

func (f *fixture) signIn(t *testing.T, email, password, code string) (auth.SignedIn, error) {
	t.Helper()
	return f.sessions.SignIn(context.Background(), auth.Credentials{Email: email, Password: password, Code: code})
}

func lastSignIn(t *testing.T, id pgtype.UUID) sql.NullTime {
	t.Helper()
	var at sql.NullTime
	err := pgtest.Pool(t).QueryRow(context.Background(), "SELECT last_sign_in_at FROM users WHERE id = $1", id).Scan(&at)
	require.NoError(t, err)
	return at
}

func TestSignInStartsASession(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)

	email := " " + strings.ToUpper(a.email) + " "
	got, err := f.signIn(t, email, password, codeAt(t, a.secret, now))
	require.NoError(t, err)

	require.True(t, platform.IsSessionToken(got.Token))
	require.Equal(t, now.Add(7*24*time.Hour), got.ExpiresAt)
	require.Equal(t, auth.User{
		ID:          a.id,
		Email:       a.email,
		DisplayName: "Daw Mi",
		Roles:       []string{"booking_admin", "site_admin"},
	}, got.Session.User)

	row := loadSession(t, got.Session.ID)
	require.True(t, row.found)
	require.Equal(t, a.id, row.userID)
	require.Equal(t, now, row.lastSeen.UTC())
	require.Equal(t, now.Add(7*24*time.Hour), row.expiresAt.UTC())
	var stored []byte
	err = pgtest.Pool(t).QueryRow(context.Background(), "SELECT token_hash FROM sessions WHERE id = $1", got.Session.ID).Scan(&stored)
	require.NoError(t, err)
	require.Equal(t, platform.HashToken(got.Token), stored, "only the token's hash is stored")

	require.Equal(t, now, lastSignIn(t, a.id).Time.UTC())
	require.Equal(t, now.Unix()/30, lastStep(t, a.id))

	session, err := f.authenticate(got.Token)
	require.NoError(t, err)
	require.Equal(t, got.Session, session)
}

// An unknown email costs an argon2 hash like any other: it is checked
// against the dummy hash, which has the production parameters.
func TestUnknownEmailIsCheckedAgainstTheDummyHash(t *testing.T) {
	require.True(t, strings.HasPrefix(auth.DummyPasswordHash, "$argon2id$v=19$m=65536,t=3,p=2$"))
	f := newFixture(t)
	a := f.newAdmin(t)
	checked := auth.RecordPasswordChecks(t)

	_, err := f.signIn(t, uniqueEmail(t), password, codeAt(t, a.secret, now))
	requireInvalidCredentials(t, err)
	require.Equal(t, []string{auth.DummyPasswordHash}, *checked)

	_, err = f.signIn(t, a.email, password+"!", codeAt(t, a.secret, now))
	requireInvalidCredentials(t, err)
	require.Equal(t, []string{auth.DummyPasswordHash, hash}, *checked)
	require.Zero(t, lastStep(t, a.id), "a wrong password claims no step")
	require.Zero(t, sessionCount(t, a.id))
}

// Even with a password that matched the dummy hash and the dummy secret's
// code, an unknown email is refused before the sign-in transaction: one
// lookup, nothing else.
func TestUnknownEmailNeverStartsASession(t *testing.T) {
	f := newFixture(t)
	auth.AcceptEveryPassword(t)
	secret, err := f.sessions.DummySecret()
	require.NoError(t, err)
	pool := pgtest.Pool(t)

	before := pool.Stat().AcquireCount()
	_, err = f.signIn(t, uniqueEmail(t), "any password at all", codeAt(t, secret, now))
	requireInvalidCredentials(t, err)
	require.Equal(t, int64(1), pool.Stat().AcquireCount()-before, "one lookup, no transaction")
}

func TestDisabledUserCannotSignIn(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	_, err := pgtest.Pool(t).Exec(context.Background(), "UPDATE users SET disabled_at = now() WHERE id = $1", a.id)
	require.NoError(t, err)

	_, err = f.signIn(t, a.email, password, codeAt(t, a.secret, now))
	requireInvalidCredentials(t, err)
	require.Zero(t, sessionCount(t, a.id))
	require.Zero(t, lastStep(t, a.id), "the refused sign-in's step claim rolled back")
	require.False(t, lastSignIn(t, a.id).Valid)
}

// The password is checked before the transaction that records the sign-in.
// If create-user replaced it in between, the sign-in must fail, or it would
// start a session create-user's sign-out never saw.
func TestRecordSignInRefusesAReplacedPassword(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	q := db.New(pgtest.Pool(t))
	ctx := context.Background()

	n, err := q.RecordSignIn(ctx, db.RecordSignInParams{ID: a.id, Now: now, PasswordHash: "$argon2id$replaced"})
	require.NoError(t, err)
	require.Zero(t, n)
	n, err = q.RecordSignIn(ctx, db.RecordSignInParams{ID: a.id, Now: now, PasswordHash: hash})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
}

// The step claim, last_sign_in_at and the session are one transaction: when
// the session cannot be saved, the code is still unused.
func TestFailedSessionInsertLeavesTheCodeUnused(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	code := codeAt(t, a.secret, now)

	t.Run("insert refused", func(t *testing.T) {
		failSessionWrites(t, "INSERT", a.id)
		_, err := f.signIn(t, a.email, password, code)
		require.ErrorContains(t, err, "refused by test")
		require.Zero(t, lastStep(t, a.id))
		require.False(t, lastSignIn(t, a.id).Valid)
	})

	_, err := f.signIn(t, a.email, password, code)
	require.NoError(t, err, "the same code signs in once the insert works")
}

func TestSameCodeTwoSignInsOneWins(t *testing.T) {
	f := newFixture(t)
	a := f.newAdmin(t)
	pool := pgtest.Pool(t)
	ctx := context.Background()

	code := codeAt(t, a.secret, now)
	for round := range 3 {
		_, err := pool.Exec(ctx, "UPDATE users SET totp_last_step = NULL WHERE id = $1", a.id)
		require.NoError(t, err)
		before := sessionCount(t, a.id)

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() {
				<-start
				_, errs[i] = f.sessions.SignIn(ctx, auth.Credentials{
					Email: a.email, Password: password, Code: code,
				})
			})
		}
		close(start)
		wg.Wait()

		winners := 0
		for _, err := range errs {
			if err == nil {
				winners++
			} else {
				requireInvalidCredentials(t, err)
			}
		}
		require.Equal(t, 1, winners, "round %d", round)
		require.Equal(t, before+1, sessionCount(t, a.id), "round %d", round)
	}
}
