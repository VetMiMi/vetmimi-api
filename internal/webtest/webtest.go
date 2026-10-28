// Package webtest serves a feature's routes behind the real middleware, on the
// test database and REDIS_URL_TEST, for handler tests.
package webtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/redistest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

const ServiceKey = "test-service-key"

// Now is the clock every handler test reads: Monday 5 October 2026, 20:30 in
// Sydney, in Sydney's offset so a time that is not converted to UTC shows.
var Now = time.Date(2026, 10, 5, 20, 30, 15, 0, time.FixedZone("AEDT", 11*60*60))

func Clock() time.Time { return Now }

type API struct {
	Router *gin.Engine
	Logs   *bytes.Buffer
	// visitors gives each public call its own address, so per-visitor limits never interfere.
	visitors int
}

// New serves what register adds, logging to a.Logs for tests that check logs hold no personal data.
func New(t *testing.T, register func(web.Routes)) *API {
	t.Helper()
	a := &API{Router: web.NewRouter(), Logs: &bytes.Buffer{}}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(a.Logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	codes, err := auth.NewTOTP(bytes.Repeat([]byte{7}, 32), Clock)
	require.NoError(t, err)
	sessions, err := auth.NewSessions(pgtest.Pool(t), codes, nil, Clock)
	require.NoError(t, err)
	register(web.NewRoutes(a.Router, ServiceKey, sessions, web.NewLimiter(liveCounter(t))))
	return a
}

// liveCounter counts under a prefix of its own and deletes its keys afterwards.
func liveCounter(t *testing.T) *ratelimit.Limiter {
	rdb := redistest.Client(t)
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	prefix := "test-" + hex.EncodeToString(b) + ":"
	t.Cleanup(func() {
		keys, err := rdb.Keys(context.Background(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	})
	return ratelimit.New(rdb, prefix, Clock)
}

// Public calls as the website's server does, with the service key and a visitor address.
func (a *API) Public(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	a.visitors++
	headers = append([]string{"X-Service-Key", ServiceKey,
		"X-Visitor-IP", fmt.Sprintf("198.51.%d.%d", a.visitors/250, a.visitors%250+1)}, headers...)
	return a.Send(method, path, body, headers...)
}

// Admin calls as the signed-in user whose session token is token.
func (a *API) Admin(method, path, token, body string) *httptest.ResponseRecorder {
	return a.Send(method, path, body, "Authorization", "Bearer "+token)
}

// Send makes a request with headers given as name, value pairs.
func (a *API) Send(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	a.Router.ServeHTTP(res, req)
	return res
}

// SignIn inserts a user holding roles and a session for them, and returns its token.
func SignIn(t *testing.T, roles ...string) string {
	t.Helper()
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	userID, err := q.CreateUser(ctx, db.CreateUserParams{
		Email: "admin-" + hex.EncodeToString(b) + "@example.com", DisplayName: "Daw Mi",
		PasswordHash: "unused", Roles: roles, TotpSecretEnc: []byte("sealed"),
	})
	require.NoError(t, err)
	return SessionFor(t, userID)
}

func SessionFor(t *testing.T, userID pgtype.UUID) string {
	t.Helper()
	token, err := tokens.NewSession()
	require.NoError(t, err)
	_, err = db.New(pgtest.Pool(t)).CreateSession(context.Background(), db.CreateSessionParams{
		UserID: userID, TokenHash: tokens.Hash(token), Now: Now, ExpiresAt: Now.Add(auth.SessionLifetime),
	})
	require.NoError(t, err)
	return token
}

// JSON asserts res has status and returns its body.
func JSON(t *testing.T, status int, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, status, res.Code, res.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body
}

// Refused asserts res is a problem with status and code, and returns it.
func Refused(t *testing.T, status int, code string, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	p := JSON(t, status, res)
	require.Equal(t, code, p["code"])
	require.Equal(t, "application/problem+json", res.Header().Get("Content-Type"))
	return p
}
