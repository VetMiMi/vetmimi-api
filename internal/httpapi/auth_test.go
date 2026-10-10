package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// Every sign-in hashes a password with 64 MiB of memory, so these tests sign
// in as few times as they can, one at a time. Tests that only need a session
// insert one directly.

const (
	testPassword = "correct horse battery"
	// testPasswordHash is testPassword hashed with the production parameters.
	testPasswordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$oLbEshmirStBvAHctylRo6RngkqGfbxRX4w4VwYm24U"
)

var totpKey = bytes.Repeat([]byte{7}, 32)

func newTOTP(t *testing.T, now clock.Now) *auth.TOTP {
	t.Helper()
	codes, err := auth.NewTOTP(totpKey, now)
	require.NoError(t, err)
	return codes
}

// newSessions authenticates sessions but, having no lockout, refuses every
// sign-in, as if Redis were down.
func newSessions(t *testing.T, now clock.Now) *auth.Sessions {
	t.Helper()
	sessions, err := auth.NewSessions(pgtest.Pool(t), newTOTP(t, now), nil, now)
	require.NoError(t, err)
	return sessions
}

func bearer(token string) []string { return []string{"Authorization", "Bearer " + token} }

func uniqueEmail(t *testing.T) string {
	t.Helper()
	b := make([]byte, 6)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return "admin-" + hex.EncodeToString(b) + "@example.com"
}

// createAdmin inserts a user holding Daw Mi's roles.
func createAdmin(t *testing.T, sealedSecret []byte) (pgtype.UUID, string) {
	t.Helper()
	return createUser(t, sealedSecret, "booking_admin", "site_admin")
}

func createUser(t *testing.T, sealedSecret []byte, roles ...string) (pgtype.UUID, string) {
	t.Helper()
	email := uniqueEmail(t)
	id, err := db.New(pgtest.Pool(t)).CreateUser(context.Background(), db.CreateUserParams{
		Email:         email,
		DisplayName:   "Daw Mi",
		PasswordHash:  testPasswordHash,
		Roles:         roles,
		TotpSecretEnc: sealedSecret,
	})
	require.NoError(t, err)
	return id, email
}

// newSession inserts an administrator and a session for them, signed in at
// the API's clock time, without hashing a password, and returns its token.
func (a *limitedAPI) newSession(t *testing.T) string {
	t.Helper()
	return insertSession(t, a.clock.at, "booking_admin", "site_admin")
}

// insertSession inserts a user holding roles and a session for them, signed
// in at now, and returns its token.
func insertSession(t *testing.T, now time.Time, roles ...string) string {
	t.Helper()
	userID, _ := createUser(t, []byte("sealed"), roles...)
	return sessionFor(t, userID, now)
}

// sessionFor inserts a session for an existing user, signed in at now, and
// returns its token.
func sessionFor(t *testing.T, userID pgtype.UUID, now time.Time) string {
	t.Helper()
	token, err := tokens.NewSession()
	require.NoError(t, err)
	_, err = db.New(pgtest.Pool(t)).CreateSession(context.Background(), db.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokens.Hash(token),
		Now:       now,
		ExpiresAt: now.Add(auth.SessionLifetime),
	})
	require.NoError(t, err)
	return token
}

// authAPI is the whole router, as cmd/api builds it, on the test database
// and REDIS_URL_TEST, logging to a buffer without timestamps, so a TOTP code
// can never turn up in one by chance.
type authAPI struct {
	handler http.Handler
	clock   *fakeClock
	codes   *auth.TOTP
	logs    *bytes.Buffer
	// meta is the Meta connector, its GraphURL set by a test that calls it.
	meta *meta.Connector
	// linkedin and assistant are set up the same way, their URLs set by a
	// test that calls them.
	linkedin  *linkedin.Connector
	assistant *assistant.Client
	// visitors numbers sign-ins, each from its own address, so the sign-in
	// limit of five a minute never interferes.
	visitors int
}

func newAuthAPI(t *testing.T) *authAPI {
	t.Helper()
	rdb, prefix := liveRedis(t)
	// The clock is in Sydney's offset, as the live host's might be, so a test
	// sees a time that is not converted to UTC.
	sydney := time.FixedZone("AEDT", 11*60*60)
	a := &authAPI{clock: &fakeClock{at: time.Date(2026, 10, 5, 20, 30, 15, 0, sydney)}, logs: &bytes.Buffer{}}
	a.codes = newTOTP(t, a.clock.now)
	log := slog.New(slog.NewJSONHandler(a.logs, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && attr.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return attr
		},
	}))
	sessions, err := auth.NewSessions(pgtest.Pool(t), a.codes, auth.NewLockout(rdb, prefix, a.clock.now), a.clock.now)
	require.NoError(t, err)
	a.meta = &meta.Connector{Pool: pgtest.Pool(t), Tokens: a.codes.Box, AppID: "app-1", AppSecret: "app-secret",
		Version: "v24.0", RedirectURL: siteOrigin + "/admin/settings/connections",
		SigningSecret: []byte("test signing secret, 32 bytes ok"), Log: log, Now: a.clock.now}
	a.linkedin = &linkedin.Connector{Pool: pgtest.Pool(t), Tokens: a.codes.Box, ClientID: "client-1",
		ClientSecret: "client-secret", Version: "202609", RedirectURL: siteOrigin + "/admin/settings/connections/linkedin",
		SigningSecret: []byte("test signing secret, 32 bytes ok"), Log: log, Now: a.clock.now}
	a.assistant = &assistant.Client{APIKey: "sk-test", Model: "claude-haiku-4-5-20251001", Log: log}
	a.handler = NewRouter(Deps{
		PingPostgres:   ok,
		PingRedis:      ok,
		Log:            log,
		ServiceKey:     testServiceKey,
		RateLimits:     NewRateLimits(ratelimit.New(rdb, prefix, a.clock.now), log, a.clock.now),
		Sessions:       sessions,
		Pool:           pgtest.Pool(t),
		SigningSecret:  []byte("test signing secret, 32 bytes ok"),
		PublicAPIURL:   "https://api.vetmimi.example",
		SiteURL:        siteOrigin,
		Media:          acceptingBucket(t),
		MediaPublicURL: "https://media.vetmimi.example",
		Meta:           a.meta,
		LinkedIn:       a.linkedin,
		Assistant:      a.assistant,
		Now:            a.clock.now,
	})
	return a
}

type admin struct {
	id     pgtype.UUID
	email  string
	secret []byte
}

func (a *authAPI) newAdmin(t *testing.T) admin {
	t.Helper()
	enrolment, err := auth.NewEnrolment("mi@example.com")
	require.NoError(t, err)
	sealed, err := a.codes.Seal(enrolment.Secret)
	require.NoError(t, err)
	id, email := createAdmin(t, sealed)
	return admin{id: id, email: email, secret: enrolment.Secret}
}

var codeOpts = totp.ValidateOpts{Period: 30, Digits: otp.DigitsSix, Algorithm: otp.AlgorithmSHA1}

// code is the admin's code at the API's clock time.
func (a *authAPI) code(t *testing.T, ad admin) string {
	t.Helper()
	code, err := totp.GenerateCodeCustom(base32.StdEncoding.EncodeToString(ad.secret), a.clock.at, codeOpts)
	require.NoError(t, err)
	return code
}

// wrongCode is a well-formed code that matches none of the steps accepted
// now.
func (a *authAPI) wrongCode(t *testing.T, ad admin) string {
	t.Helper()
	accepted := map[string]bool{}
	for _, offset := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := totp.GenerateCodeCustom(base32.StdEncoding.EncodeToString(ad.secret), a.clock.at.Add(offset), codeOpts)
		require.NoError(t, err)
		accepted[code] = true
	}
	n := 0
	for accepted[fmt.Sprintf("%06d", n)] {
		n++
	}
	return fmt.Sprintf("%06d", n)
}

func (a *authAPI) signIn(t *testing.T, email, password, code string) *httptest.ResponseRecorder {
	t.Helper()
	fields := map[string]string{"email": email, "password": password}
	if code != "" {
		fields["totpCode"] = code
	}
	body, err := json.Marshal(fields)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/auth/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Service-Key", testServiceKey)
	a.visitors++
	req.Header.Set("X-Visitor-IP", fmt.Sprintf("198.51.100.%d", a.visitors))
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

func (a *authAPI) send(method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

// The SessionCreated.token pattern in openapi.yaml.
var sessionTokenPattern = regexp.MustCompile(`^vms_[A-Za-z0-9_-]{43}$`)

type sessionCreated struct {
	Token     string         `json:"token"`
	ExpiresAt time.Time      `json:"expiresAt"`
	User      map[string]any `json:"user"`
}

func signedIn(t *testing.T, res *httptest.ResponseRecorder) sessionCreated {
	t.Helper()
	require.Equal(t, http.StatusCreated, res.Code, res.Body.String())
	var body sessionCreated
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body
}

func TestSignInReadTheUserAndSignOut(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)

	res := a.signIn(t, strings.ToUpper(ad.email), testPassword, a.code(t, ad))
	created := signedIn(t, res)
	require.Regexp(t, sessionTokenPattern, created.Token)
	require.Contains(t, res.Body.String(), `"expiresAt":"2026-10-12T09:30:15Z"`, "seven days on, in UTC")
	user := map[string]any{
		"id":             ad.id.String(),
		"email":          ad.email,
		"displayName":    "Daw Mi",
		"roles":          []any{"booking_admin", "site_admin"},
		"isPractitioner": false,
		"twoStepEnabled": true,
	}
	require.Equal(t, user, created.User)

	res = a.send(http.MethodGet, "/auth/me", created.Token)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var me map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &me))
	require.Equal(t, user, me)

	res = a.send(http.MethodDelete, "/auth/sessions/current", created.Token)
	require.Equal(t, http.StatusNoContent, res.Code, res.Body.String())
	require.Empty(t, res.Body.String())

	requireUnauthenticated(t, a.send(http.MethodGet, "/auth/me", created.Token))
	requireUnauthenticated(t, a.send(http.MethodDelete, "/auth/sessions/current", created.Token))
}

// A password-only user signs in without a code and /auth/me says so, so the
// website can suggest two-step sign-in.
func TestPasswordOnlySignIn(t *testing.T) {
	a := newAuthAPI(t)
	_, email := createAdmin(t, nil)

	created := signedIn(t, a.signIn(t, email, testPassword, ""))
	require.Equal(t, false, created.User["twoStepEnabled"])
	res := a.send(http.MethodGet, "/auth/me", created.Token)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"twoStepEnabled":false`)
}

// Unknown email, wrong password, wrong or missing code, replayed code, a
// disabled user and a password-only user's wrong password all get the very
// same bytes, so a caller learns nothing about which.
func TestSignInFailuresLookIdentical(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	_, passwordOnly := createAdmin(t, nil)
	disabled := a.newAdmin(t)
	_, err := pgtest.Pool(t).Exec(context.Background(), "UPDATE users SET disabled_at = now() WHERE id = $1", disabled.id)
	require.NoError(t, err)

	code := a.code(t, ad)
	failures := map[string]func() *httptest.ResponseRecorder{
		"unknown email":  func() *httptest.ResponseRecorder { return a.signIn(t, uniqueEmail(t), testPassword, code) },
		"wrong password": func() *httptest.ResponseRecorder { return a.signIn(t, ad.email, testPassword+"!", code) },
		"wrong code":     func() *httptest.ResponseRecorder { return a.signIn(t, ad.email, testPassword, a.wrongCode(t, ad)) },
		"missing code":   func() *httptest.ResponseRecorder { return a.signIn(t, ad.email, testPassword, "") },
		"password-only, wrong password": func() *httptest.ResponseRecorder {
			return a.signIn(t, passwordOnly, testPassword+"!", "")
		},
		"disabled user": func() *httptest.ResponseRecorder {
			return a.signIn(t, disabled.email, testPassword, a.code(t, disabled))
		},
		"replayed code": func() *httptest.ResponseRecorder {
			signedIn(t, a.signIn(t, ad.email, testPassword, code))
			return a.signIn(t, ad.email, testPassword, code)
		},
	}
	var first []byte
	for _, name := range []string{"unknown email", "wrong password", "wrong code", "missing code", "password-only, wrong password", "disabled user", "replayed code"} {
		res := failures[name]()
		require.Equal(t, http.StatusUnauthorized, res.Code, "%s: %s", name, res.Body.String())
		require.Equal(t, "invalid_credentials", problemFrom(t, res)["code"], name)
		if first == nil {
			first = res.Body.Bytes()
			continue
		}
		require.Equal(t, string(first), res.Body.String(), name)
	}
}

func TestAuthLogsHoldNoEmailPasswordCodeOrToken(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	wrong := a.wrongCode(t, ad)
	code := a.code(t, ad)

	res := a.signIn(t, ad.email, testPassword+"!", wrong)
	require.Equal(t, http.StatusUnauthorized, res.Code)
	created := signedIn(t, a.signIn(t, ad.email, testPassword, code))
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, "/auth/me", created.Token).Code)
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, "/auth/sessions/current", created.Token).Code)

	logs := a.logs.String()
	var events []string
	for _, line := range logLines(t, logs) {
		events = append(events, line["msg"].(string))
		if line["msg"] == "signed_in" || line["msg"] == "signed_out" {
			require.Equal(t, ad.id.String(), line["user_id"], line["msg"])
		}
	}
	require.Contains(t, events, "signed_in")
	require.Contains(t, events, "signed_out")
	for _, secret := range []string{
		ad.email, strings.Split(ad.email, "@")[0], testPassword, wrong, code,
		created.Token, strings.TrimPrefix(created.Token, "vms_"), testServiceKey,
	} {
		require.NotContains(t, logs, secret)
	}
}

func TestSignedInRoutesNeedALiveSession(t *testing.T) {
	a := newLimitedAPI(t, nil, "")
	live := a.newSession(t)
	dead := strings.Replace(live, live[10:14], "AAAA", 1)
	if dead == live {
		dead = strings.Replace(live, live[10:14], "BBBB", 1)
	}

	for name, headers := range map[string][]string{
		"no header":        nil,
		"empty bearer":     {"Authorization", "Bearer "},
		"another scheme":   {"Authorization", "Basic " + live},
		"no scheme":        {"Authorization", live},
		"malformed token":  bearer("vms_short"),
		"unknown token":    bearer(dead),
		"service key only": {"X-Service-Key", testServiceKey},
	} {
		requireUnauthenticated(t, a.send(http.MethodGet, meURL, headers...))
		require.NotContains(t, a.logs.String(), live[4:], name)
	}
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, meURL, bearer(live)...).Code)
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, meURL, "Authorization", "bearer "+live).Code,
		"the scheme is case-insensitive")
}

// A router built without Sessions, as most tests build it, refuses every
// signed-in call instead of failing on it.
func TestRouterWithoutSessionsRefusesSignedInCalls(t *testing.T) {
	token, err := tokens.NewSession()
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	NewRouter(Deps{Log: quiet}).ServeHTTP(res, req)
	requireUnauthenticated(t, res)
}

// The per-email sign-in limits through the whole router. Every attempt here
// hashes a password, one at a time.

// Nine failures leave the email one short of a lockout: the right password
// still signs in.
func TestNineFailuresThenTheRightPasswordSignsIn(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	for i := range 9 {
		res := a.signIn(t, ad.email, testPassword+"!", a.code(t, ad))
		require.Equal(t, http.StatusUnauthorized, res.Code, "failure %d: %s", i+1, res.Body.String())
	}
	signedIn(t, a.signIn(t, ad.email, testPassword, a.code(t, ad)))
}

// Ten failures lock an email whether or not it has an account, and the
// answer to the next attempt, even with the right password, gives nothing
// away: the same bytes, the same Retry-After, and a log line naming the email
// by a hash prefix only.
func TestLockedEmailAnswersTheSameWithOrWithoutAnAccount(t *testing.T) {
	a := newAuthAPI(t)
	ad := a.newAdmin(t)
	unknown := uniqueEmail(t)
	code := a.code(t, ad)

	var bodies []string
	var waits []int
	for _, email := range []string{ad.email, unknown} {
		for i := range 10 {
			res := a.signIn(t, email, testPassword+"!", code)
			require.Equal(t, http.StatusUnauthorized, res.Code, "failure %d: %s", i+1, res.Body.String())
		}
		res := a.signIn(t, email, testPassword, code)
		require.Equal(t, http.StatusTooManyRequests, res.Code, res.Body.String())
		require.Equal(t, "rate_limited", problemFrom(t, res)["code"])
		wait, err := strconv.Atoi(res.Header().Get("Retry-After"))
		require.NoError(t, err)
		bodies = append(bodies, res.Body.String())
		waits = append(waits, wait)
	}
	require.Equal(t, bodies[0], bodies[1])
	require.InDelta(t, waits[0], waits[1], 1)

	logs := a.logs.String()
	var locked []any
	for _, line := range logLines(t, logs) {
		if line["msg"] == "sign_in_locked" {
			locked = append(locked, line["email_hash"])
		}
	}
	require.Equal(t, []any{auth.EmailHashPrefix(ad.email), auth.EmailHashPrefix(unknown)}, locked)
	for _, secret := range []string{
		ad.email, strings.Split(ad.email, "@")[0], unknown, strings.Split(unknown, "@")[0], testPassword, code,
	} {
		require.NotContains(t, logs, secret)
	}

	a.clock.at = a.clock.at.Add(time.Hour)
	signedIn(t, a.signIn(t, ad.email, testPassword, a.code(t, ad)))
}
