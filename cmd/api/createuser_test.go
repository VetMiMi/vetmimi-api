package main

import (
	"bytes"
	"context"
	"encoding/base32"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var (
	now     = time.Date(2026, 10, 5, 9, 0, 15, 0, time.UTC)
	testKey = bytes.Repeat([]byte{7}, 32)
)

const password = "correct horse battery"

var uriPattern = regexp.MustCompile(`otpauth://\S+`)

var codeOpts = totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// typist answers create-user's code prompts: wrong codes first, then, if
// right is set, the code for the secret in the URI create-user printed. Each
// Read is one line, which is all bufio.Scanner asks for per prompt.
type typist struct {
	out   *bytes.Buffer
	wrong int
	right bool
}

func (ty *typist) Read(p []byte) (int, error) {
	key, err := otp.NewKeyFromURL(uriPattern.FindString(ty.out.String()))
	if err != nil {
		return 0, err
	}
	codes := map[string]bool{}
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := totp.GenerateCodeCustom(key.Secret(), now.Add(offset), codeOpts)
		if err != nil {
			return 0, err
		}
		codes[code] = true
	}
	switch {
	case ty.wrong > 0:
		ty.wrong--
		wrong := 0
		for codes[fmt.Sprintf("%06d", wrong)] {
			wrong++
		}
		return copy(p, fmt.Sprintf("%06d\n", wrong)), nil
	case ty.right:
		ty.right = false
		code, _ := totp.GenerateCodeCustom(key.Secret(), now, codeOpts)
		return copy(p, " "+code+"\n"), nil
	}
	return 0, io.EOF
}

type result struct {
	out  string
	logs string
	err  error
}

func createUserWith(t *testing.T, f userFlags, passwords []string, ty *typist) result {
	t.Helper()
	codes, err := auth.NewTOTP(testKey, func() time.Time { return now })
	require.NoError(t, err)
	var out, logs bytes.Buffer
	ty.out = &out
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	err = createUser(context.Background(), log, db.New(pgtest.Pool(t)), codes, f, terminal{
		in:           ty,
		out:          &out,
		readPassword: typedPasswords(passwords),
	})
	return result{out: out.String(), logs: logs.String(), err: err}
}

func typedPasswords(passwords []string) func() ([]byte, error) {
	return func() ([]byte, error) {
		if len(passwords) == 0 {
			return nil, io.EOF
		}
		p := passwords[0]
		passwords = passwords[1:]
		return []byte(p), nil
	}
}

type stored struct {
	id             pgtype.UUID
	displayName    string
	passwordHash   string
	roles          []string
	isPractitioner bool
	totpSecretEnc  []byte
}

// lookup returns the user with email, or ok false when there is none.
func lookup(t *testing.T, email string) (u stored, ok bool) {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(),
		`SELECT id, display_name, password_hash, roles, is_practitioner, totp_secret_enc FROM users WHERE email = $1`, email)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		require.NoError(t, rows.Scan(&u.id, &u.displayName, &u.passwordHash, &u.roles, &u.isPractitioner, &u.totpSecretEnc))
		ok = true
	}
	require.NoError(t, rows.Err())
	return u, ok
}

var emails int

// newFlags gives each test an email of its own: they share one database.
func newFlags(t *testing.T) userFlags {
	emails++
	return userFlags{
		email: fmt.Sprintf("%s-%d-%d@example.com", strings.ToLower(t.Name()), time.Now().UnixNano(), emails),
		name:  "Daw Mi",
		roles: "site_admin",
	}
}

func TestCreateUserSavesAfterAValidCode(t *testing.T) {
	f := newFlags(t)
	f.email = " " + strings.ToUpper(f.email) + " "
	r := createUserWith(t, f, []string{password, password}, &typist{right: true})
	require.NoError(t, r.err)
	require.True(t, strings.HasSuffix(r.out, "created\n"), r.out)

	email := strings.ToLower(strings.TrimSpace(f.email))
	u, ok := lookup(t, email)
	require.True(t, ok)
	require.Equal(t, "Daw Mi", u.displayName)
	require.Equal(t, []string{"site_admin"}, u.roles)
	require.False(t, u.isPractitioner)
	ok, err := auth.VerifyPassword(password, u.passwordHash)
	require.NoError(t, err)
	require.True(t, ok)

	key, err := otp.NewKeyFromURL(uriPattern.FindString(r.out))
	require.NoError(t, err)
	require.Equal(t, "VetMiMi", key.Issuer())
	require.Equal(t, email, key.AccountName())
	codes, err := auth.NewTOTP(testKey, func() time.Time { return now })
	require.NoError(t, err)
	secret, err := codes.Open(u.totpSecretEnc)
	require.NoError(t, err)
	require.Equal(t, key.Secret(), base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret), "the stored secret is the one in the URI")
}

func TestCreateUserUpdatesAnExistingEmail(t *testing.T) {
	f := newFlags(t)
	first := createUserWith(t, f, []string{password, password}, &typist{right: true})
	require.NoError(t, first.err)
	before, _ := lookup(t, f.email)

	f.email = strings.ToUpper(f.email)
	f.name = "Mi"
	f.roles = "booking_admin,content_editor"
	second := createUserWith(t, f, []string{"another long password", "another long password"}, &typist{right: true})
	require.NoError(t, second.err)
	require.True(t, strings.HasSuffix(second.out, "updated\n"), second.out)

	after, ok := lookup(t, strings.ToLower(f.email))
	require.True(t, ok)
	require.Equal(t, before.id, after.id)
	require.Equal(t, "Mi", after.displayName)
	require.Equal(t, []string{"booking_admin", "content_editor"}, after.roles)
	require.NotEqual(t, before.passwordHash, after.passwordHash)
	require.NotEqual(t, before.totpSecretEnc, after.totpSecretEnc)
}

func TestCreateUserAllowsThreeTries(t *testing.T) {
	f := newFlags(t)
	r := createUserWith(t, f, []string{password, password}, &typist{wrong: 2, right: true})
	require.NoError(t, r.err)
	require.Equal(t, 2, strings.Count(r.out, "That code is not valid."))
	_, ok := lookup(t, f.email)
	require.True(t, ok)
}

func TestThreeWrongCodesSaveNothing(t *testing.T) {
	f := newFlags(t)
	r := createUserWith(t, f, []string{password, password}, &typist{wrong: 3, right: true})
	require.ErrorContains(t, r.err, "3 wrong codes; nothing was saved")
	_, ok := lookup(t, f.email)
	require.False(t, ok)

	r = createUserWith(t, f, []string{password, password}, &typist{})
	require.ErrorContains(t, r.err, "no code entered")
	_, ok = lookup(t, f.email)
	require.False(t, ok)
}

func TestPasswordMismatchSavesNothing(t *testing.T) {
	f := newFlags(t)
	r := createUserWith(t, f, []string{password, password + "!"}, &typist{right: true})
	require.ErrorContains(t, r.err, "the passwords do not match")
	require.NotContains(t, r.out, "otpauth://", "no secret is generated for a refused password")
	_, ok := lookup(t, f.email)
	require.False(t, ok)
}

func TestPasswordBounds(t *testing.T) {
	for _, tc := range []struct {
		password string
		ok       bool
	}{
		{strings.Repeat("a", 11), false},
		{strings.Repeat("a", 12), true},
		{strings.Repeat("a", 200), true},
		{strings.Repeat("ü", 200), true}, // 400 bytes: the bound is characters
		{strings.Repeat("a", 201), false},
	} {
		got, err := askPassword(terminal{out: io.Discard, readPassword: typedPasswords([]string{tc.password, tc.password})})
		if tc.ok {
			require.NoError(t, err, len(tc.password))
			require.Equal(t, tc.password, got)
		} else {
			require.ErrorContains(t, err, "the password must be 12 to 200 characters", len(tc.password))
		}
	}
}

func TestCreateUserRefusesASecondPractitioner(t *testing.T) {
	pool := pgtest.Pool(t)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(), "DELETE FROM users WHERE is_practitioner")
		require.NoError(t, err)
	})
	mi := newFlags(t)
	mi.practitioner = true
	require.NoError(t, createUserWith(t, mi, []string{password, password}, &typist{right: true}).err)

	other := newFlags(t)
	other.practitioner = true
	r := createUserWith(t, other, []string{password, password}, &typist{right: true})
	require.ErrorIs(t, r.err, auth.ErrPractitionerTaken)
	require.EqualError(t, r.err, "create-user: another user is already the practitioner; there can be only one")
	_, ok := lookup(t, other.email)
	require.False(t, ok)
}

func TestCreateUserLogsOnlyTheUserID(t *testing.T) {
	f := newFlags(t)
	r := createUserWith(t, f, []string{password, password}, &typist{right: true})
	require.NoError(t, r.err)
	u, _ := lookup(t, f.email)

	require.Contains(t, r.logs, `"user_id":"`+u.id.String()+`"`)
	uri := uriPattern.FindString(r.out)
	key, err := otp.NewKeyFromURL(uri)
	require.NoError(t, err)
	for _, secret := range []string{f.email, "example.com", password, key.Secret(), uri, "Daw Mi"} {
		require.NotContains(t, r.logs, secret)
	}
}

func TestUserFlagsAreChecked(t *testing.T) {
	valid := userFlags{email: " Mi@Example.COM ", name: " Daw Mi ", roles: "site_admin, booking_admin,site_admin"}
	a, err := valid.account()
	require.NoError(t, err)
	require.Equal(t, "mi@example.com", a.Email)
	require.Equal(t, "Daw Mi", a.DisplayName)
	require.Equal(t, []string{"site_admin", "booking_admin"}, a.Roles)

	long := strings.Repeat("a", 243) + "@example.com" // 255 characters
	for name, f := range map[string]userFlags{
		"no email":        {name: "Mi", roles: "site_admin"},
		"not an email":    {email: "mi", name: "Mi", roles: "site_admin"},
		"display name":    {email: "Mi <mi@example.com>", name: "Mi", roles: "site_admin"},
		"email too long":  {email: long, name: "Mi", roles: "site_admin"},
		"no name":         {email: "mi@example.com", name: "  ", roles: "site_admin"},
		"name too long":   {email: "mi@example.com", name: strings.Repeat("a", 121), roles: "site_admin"},
		"no roles":        {email: "mi@example.com", name: "Mi"},
		"unknown role":    {email: "mi@example.com", name: "Mi", roles: "site_admin,owner"},
		"empty role item": {email: "mi@example.com", name: "Mi", roles: "site_admin,"},
	} {
		_, err := f.account()
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), "mi@example.com", name)
	}

	_, err = userFlags{email: "mi@example.com", name: strings.Repeat("ü", 120), roles: "site_admin"}.account()
	require.NoError(t, err, "120 characters is the limit, not 120 bytes")
	_, err = userFlags{email: strings.Repeat("a", 242) + "@example.com", name: "Mi", roles: "site_admin"}.account()
	require.NoError(t, err, "254 characters is the limit")
}
