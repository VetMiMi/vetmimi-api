package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pquerna/otp/totp"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

// passwordHash is password hashed with the production parameters, so tests skip hashing it when they insert a user.
const passwordHash = "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHRzYWx0c2FsdA$oLbEshmirStBvAHctylRo6RngkqGfbxRX4w4VwYm24U"

func authAPI(t *testing.T) *webtest.API {
	h := &authHandler{Sessions: webtest.Sessions(t)}
	return webtest.New(t, h.Routes)
}

func uniqueEmail() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "admin-" + hex.EncodeToString(b) + "@example.com"
}

// insertAdmin adds a user holding Daw Mi's roles, enrolled in two-step sign-in when secret is set.
func insertAdmin(t *testing.T, secret []byte) (pgtype.UUID, string) {
	t.Helper()
	var sealed []byte
	if secret != nil {
		codes, err := auth.NewTOTP(webtest.TOTPKey, webtest.Clock)
		require.NoError(t, err)
		sealed, err = codes.Seal(secret)
		require.NoError(t, err)
	}
	email := uniqueEmail()
	id, err := db.New(pgtest.Pool(t)).CreateUser(context.Background(), db.CreateUserParams{
		Email: email, DisplayName: "Daw Mi", PasswordHash: passwordHash,
		Roles: []string{"booking_admin", "site_admin"}, TotpSecretEnc: sealed,
	})
	require.NoError(t, err)
	return id, email
}

func signIn(t *testing.T, a *webtest.API, email, password, code string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]string{"email": email, "password": password, "totpCode": code})
	require.NoError(t, err)
	return a.Public(http.MethodPost, "/auth/sessions", string(body))
}

func TestHTTP_SignInReadMeAndSignOut(t *testing.T) {
	enrolment, err := auth.NewEnrolment("mi@example.com")
	require.NoError(t, err)
	id, email := insertAdmin(t, enrolment.Secret)
	code, err := totp.GenerateCodeCustom(base32.StdEncoding.EncodeToString(enrolment.Secret), webtest.Now, codeOpts)
	require.NoError(t, err)
	a := authAPI(t)

	res := signIn(t, a, strings.ToUpper(email), password, code)
	created := webtest.JSON(t, http.StatusCreated, res)
	token := created["token"].(string)
	require.Regexp(t, `^vms_[A-Za-z0-9_-]{43}$`, token)
	require.Equal(t, "2026-10-12T09:30:15Z", created["expiresAt"], "seven days on, in UTC")
	user := map[string]any{
		"id": id.String(), "email": email, "displayName": "Daw Mi", "roles": []any{"booking_admin", "site_admin"},
		"isPractitioner": false, "twoStepEnabled": true,
	}
	require.Equal(t, user, created["user"])

	require.Equal(t, user, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/auth/me", token, "")))
	res = a.Admin(http.MethodDelete, "/auth/sessions/current", token, "")
	require.Equal(t, http.StatusNoContent, res.Code, res.Body.String())
	require.Empty(t, res.Body.String())
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Admin(http.MethodGet, "/auth/me", token, ""))
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Admin(http.MethodDelete, "/auth/sessions/current", token, ""))

	logs := a.Logs.String()
	require.Contains(t, logs, `"msg":"signed_in"`)
	require.Contains(t, logs, `"msg":"signed_out"`)
	for _, secret := range []string{email, strings.Split(email, "@")[0], password, code, token, webtest.ServiceKey} {
		require.NotContains(t, logs, secret)
	}
}

func TestHTTP_SignInFailures(t *testing.T) {
	_, email := insertAdmin(t, nil)
	a := authAPI(t)

	wrong := signIn(t, a, email, password+"!", "")
	webtest.Refused(t, http.StatusUnauthorized, "invalid_credentials", wrong)
	unknown := signIn(t, a, uniqueEmail(), password, "")
	require.Equal(t, wrong.Body.String(), unknown.Body.String(), "a caller learns nothing about which failed")

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", signIn(t, a, email, "short", "12345x"))
	require.Equal(t, []any{
		map[string]any{"field": "/password", "message": "must be at least 12 characters"},
		map[string]any{"field": "/totpCode", "message": "must be six digits"},
	}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Public(http.MethodPost, "/auth/sessions", `{"email": "`+email+`", "password": "`+password+`", "remember": true}`))
	require.Equal(t, "/remember", p["errors"].([]any)[0].(map[string]any)["field"])
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated",
		a.Send(http.MethodPost, "/auth/sessions", `{"email": "`+email+`", "password": "`+password+`"}`))

	created := webtest.JSON(t, http.StatusCreated, signIn(t, a, email, password, ""))
	require.Equal(t, false, created["user"].(map[string]any)["twoStepEnabled"], "password-only signs in without a code")
}

// The eleventh attempt on an email is locked out, and the log names it by a hash prefix only.
func TestHTTP_LockedEmail(t *testing.T) {
	email := uniqueEmail()
	a := authAPI(t)
	for i := range 10 {
		res := signIn(t, a, email, password, "")
		require.Equal(t, http.StatusUnauthorized, res.Code, "attempt %d: %s", i+1, res.Body.String())
	}

	res := signIn(t, a, email, password, "")
	webtest.Refused(t, http.StatusTooManyRequests, "rate_limited", res)
	wait, err := strconv.Atoi(res.Header().Get("Retry-After"))
	require.NoError(t, err)
	require.Positive(t, wait)
	require.Contains(t, a.Logs.String(), `"email_hash":"`+auth.EmailHashPrefix(email)+`"`)
	require.NotContains(t, a.Logs.String(), strings.Split(email, "@")[0])
}

func TestHTTP_SignedInRoutesNeedALiveSession(t *testing.T) {
	a := authAPI(t)
	editor := webtest.SignIn(t, "content_editor")
	me := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/auth/me", editor, ""))
	require.Equal(t, []any{"content_editor"}, me["roles"])

	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, "/auth/me", ""))
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Admin(http.MethodGet, "/auth/me", "vms_short", ""))
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated",
		a.Send(http.MethodDelete, "/auth/sessions/current", "", "X-Service-Key", webtest.ServiceKey))
	require.NotContains(t, a.Logs.String(), editor)
}
