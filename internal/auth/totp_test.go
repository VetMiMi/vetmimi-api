package auth_test

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// now is in the middle of a 30-second step, so ±30 s lands in the steps
// either side and ±60 s two steps away.
var now = time.Date(2026, 10, 5, 9, 0, 15, 0, time.UTC)

var testKey = bytes.Repeat([]byte{7}, 32)

func newTOTP(t *testing.T, key []byte) *auth.TOTP {
	t.Helper()
	codes, err := auth.NewTOTP(key, func() time.Time { return now })
	require.NoError(t, err)
	return codes
}

func newSecret(t *testing.T) []byte {
	t.Helper()
	enrolment, err := auth.NewEnrolment("mi@example.com")
	require.NoError(t, err)
	return enrolment.Secret
}

func codeAt(t *testing.T, secret []byte, at time.Time) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(base32.StdEncoding.EncodeToString(secret), at,
		totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1})
	require.NoError(t, err)
	return code
}

func TestEnrolmentURIDescribesTheSecret(t *testing.T) {
	enrolment, err := auth.NewEnrolment("mi@example.com")
	require.NoError(t, err)
	require.Len(t, enrolment.Secret, 20)

	u, err := url.Parse(enrolment.URI)
	require.NoError(t, err)
	require.Equal(t, "otpauth", u.Scheme)
	require.Equal(t, "totp", u.Host)
	require.Equal(t, "/VetMiMi:mi@example.com", u.Path)
	q := u.Query()
	require.Equal(t, "VetMiMi", q.Get("issuer"))
	require.Equal(t, "SHA1", q.Get("algorithm"))
	require.Equal(t, "6", q.Get("digits"))
	require.Equal(t, "30", q.Get("period"))
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(q.Get("secret"))
	require.NoError(t, err)
	require.Equal(t, enrolment.Secret, secret)
}

func TestCodeMatchesWithinOneStep(t *testing.T) {
	codes := newTOTP(t, testKey)
	secret := newSecret(t)
	current := now.Unix() / 30

	for offset, step := range map[time.Duration]int64{0: current, -30 * time.Second: current - 1, 30 * time.Second: current + 1} {
		got, ok := codes.Match(secret, codeAt(t, secret, now.Add(offset)))
		require.True(t, ok, offset)
		require.Equal(t, step, got, offset)
	}
	for _, offset := range []time.Duration{-60 * time.Second, 60 * time.Second} {
		_, ok := codes.Match(secret, codeAt(t, secret, now.Add(offset)))
		require.False(t, ok, offset)
	}
	_, ok := codes.Match(secret, "")
	require.False(t, ok)
}

func TestSealedSecretOpensOnlyUnderItsKey(t *testing.T) {
	codes := newTOTP(t, testKey)
	secret := newSecret(t)

	sealed, err := codes.Seal(secret)
	require.NoError(t, err)
	require.Len(t, sealed, 12+20+16, "nonce, secret, GCM tag")
	again, err := codes.Seal(secret)
	require.NoError(t, err)
	require.NotEqual(t, sealed[:12], again[:12], "every seal has a fresh nonce")

	opened, err := codes.Open(sealed)
	require.NoError(t, err)
	require.Equal(t, secret, opened)

	_, err = newTOTP(t, bytes.Repeat([]byte{8}, 32)).Open(sealed)
	require.Error(t, err, "another key")

	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 1
	_, err = codes.Open(tampered)
	require.Error(t, err, "altered ciphertext")

	_, err = codes.Open(sealed[:5])
	require.Error(t, err, "shorter than a nonce")
}

func TestNewTOTPNeedsAnAES256Key(t *testing.T) {
	for _, n := range []int{0, 16, 31, 33} {
		_, err := auth.NewTOTP(make([]byte, n), time.Now)
		require.Error(t, err, n)
	}
}

// enrolled inserts a user with a fresh TOTP secret.
func enrolled(t *testing.T, q db.Querier, codes *auth.TOTP) (pgtype.UUID, []byte) {
	t.Helper()
	secret := newSecret(t)
	sealed, err := codes.Seal(secret)
	require.NoError(t, err)
	id, err := q.CreateUser(context.Background(), db.CreateUserParams{
		Email:         uniqueEmail(t),
		DisplayName:   "Test",
		PasswordHash:  hash,
		Roles:         []string{"booking_admin"},
		TotpSecretEnc: sealed,
	})
	require.NoError(t, err)
	return id, secret
}

func requireInvalidCredentials(t *testing.T, err error) {
	t.Helper()
	var appErr *apperr.Error
	require.True(t, errors.As(err, &appErr), "got %v", err)
	require.Equal(t, apperr.InvalidCredentials, appErr.Code)
	require.Empty(t, appErr.Detail, "a TOTP refusal must read exactly like a wrong password")
}

func TestVerifyTOTPReturnsTheMatchedStep(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	codes := newTOTP(t, testKey)
	id, secret := enrolled(t, q, codes)

	step, err := auth.VerifyTOTP(context.Background(), q, codes, id, codeAt(t, secret, now.Add(-30*time.Second)))
	require.NoError(t, err)
	require.Equal(t, now.Unix()/30-1, step)
	require.Equal(t, step, lastStep(t, id))
}

func TestVerifyTOTPRefusesAWrongCodeOrUnknownUser(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	codes := newTOTP(t, testKey)
	id, secret := enrolled(t, q, codes)

	_, err := auth.VerifyTOTP(context.Background(), q, codes, id, codeAt(t, secret, now.Add(60*time.Second)))
	requireInvalidCredentials(t, err)
	require.Zero(t, lastStep(t, id), "a refused code claims no step")

	_, err = auth.VerifyTOTP(context.Background(), q, codes, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, codeAt(t, secret, now))
	requireInvalidCredentials(t, err)
}

func TestTOTPReplayIsRefused(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	codes := newTOTP(t, testKey)
	id, secret := enrolled(t, q, codes)
	ctx := context.Background()

	code := codeAt(t, secret, now)
	_, err := auth.VerifyTOTP(ctx, q, codes, id, code)
	require.NoError(t, err)
	_, err = auth.VerifyTOTP(ctx, q, codes, id, code)
	requireInvalidCredentials(t, err)

	// The previous step's code is still inside the window, but older than
	// the step already accepted.
	_, err = auth.VerifyTOTP(ctx, q, codes, id, codeAt(t, secret, now.Add(-30*time.Second)))
	requireInvalidCredentials(t, err)

	_, err = auth.VerifyTOTP(ctx, q, codes, id, codeAt(t, secret, now.Add(30*time.Second)))
	require.NoError(t, err, "a later step is still accepted")
}

// The code typed at enrolment was seen on the administrator's screen; it must
// not also work as a sign-in code, whether the account is new or re-enrolled.
func TestEnrolmentCodeCannotSignIn(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	codes := newTOTP(t, testKey)
	ctx := context.Background()
	a := account(uniqueEmail(t))

	for _, wantCreated := range []bool{true, false} {
		secret := newSecret(t)
		code := codeAt(t, secret, now)
		step, ok := codes.Match(secret, code)
		require.True(t, ok)
		sealed, err := codes.Seal(secret)
		require.NoError(t, err)
		a.SealedTOTPSecret, a.EnrolmentStep = sealed, step

		id, created, err := auth.SaveAccount(ctx, q, a)
		require.NoError(t, err)
		require.Equal(t, wantCreated, created)

		_, err = auth.VerifyTOTP(ctx, q, codes, id, code)
		requireInvalidCredentials(t, err)
		_, err = auth.VerifyTOTP(ctx, q, codes, id, codeAt(t, secret, now.Add(30*time.Second)))
		require.NoError(t, err, "the next code signs in")
	}
}

func TestConcurrentTOTPUseHasOneWinner(t *testing.T) {
	pool := pgtest.Pool(t)
	q := db.New(pool)
	codes := newTOTP(t, testKey)
	id, secret := enrolled(t, q, codes)
	code := codeAt(t, secret, now)
	ctx := context.Background()

	for round := range 20 {
		_, err := pool.Exec(ctx, "UPDATE users SET totp_last_step = NULL WHERE id = $1", id)
		require.NoError(t, err)

		var wg sync.WaitGroup
		start := make(chan struct{})
		errs := make([]error, 2)
		for i := range errs {
			wg.Go(func() {
				<-start
				_, errs[i] = auth.VerifyTOTP(ctx, q, codes, id, code)
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
	}
}

func lastStep(t *testing.T, id pgtype.UUID) int64 {
	t.Helper()
	var step pgtype.Int8
	err := pgtest.Pool(t).QueryRow(context.Background(), "SELECT totp_last_step FROM users WHERE id = $1", id).Scan(&step)
	require.NoError(t, err)
	return step.Int64
}
