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
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
	"github.com/VetMiMi/vetmimi-api/internal/redistest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type API struct {
	Router *gin.Engine
	Logs   *bytes.Buffer
	// visitors gives each public call its own address, so per-visitor limits never interfere.
	visitors int
}

const ServiceKey = "test-service-key"

// Now is the clock every handler test reads: Monday 5 October 2026, 20:30 in
// Sydney, in Sydney's offset so a time that is not converted to UTC shows.
var Now = time.Date(2026, 10, 5, 20, 30, 15, 0, time.FixedZone("AEDT", 11*60*60))

// TOTPKey seals the TOTP secrets of the users Sessions signs in.
var TOTPKey = bytes.Repeat([]byte{7}, 32)

func Clock() time.Time {
	return Now
}

// New serves what register adds, logging to Logs for tests that check logs hold no personal data.
func New(t *testing.T, register func(web.Routes)) *API {
	t.Helper()
	api := &API{
		Router: web.NewRouter(),
		Logs:   &bytes.Buffer{},
	}
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(api.Logs, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	rdb, prefix := liveRedis(t)
	limiter := web.NewLimiter(ratelimit.New(rdb, prefix, Clock))
	routes := web.NewRoutes(api.Router, ServiceKey, Sessions(t), limiter)
	register(routes)
	return api
}

// Sessions signs users in on the test database, counting failures on REDIS_URL_TEST.
func Sessions(t *testing.T) *auth.Sessions {
	t.Helper()
	codes, err := auth.NewTOTP(TOTPKey, Clock)
	require.NoError(t, err)
	rdb, prefix := liveRedis(t)
	sessions, err := auth.NewSessions(pgtest.Pool(t), codes, auth.NewLockout(rdb, prefix, Clock), Clock)
	require.NoError(t, err)
	return sessions
}

// Public calls as the website's server does, with the service key and a visitor address.
func (api *API) Public(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	api.visitors++
	visitorIP := fmt.Sprintf("198.51.%d.%d", api.visitors/250, api.visitors%250+1)
	headers = append([]string{"X-Service-Key", ServiceKey, "X-Visitor-IP", visitorIP}, headers...)
	return api.Send(method, path, body, headers...)
}

// Admin calls as the signed-in user whose session token is token.
func (api *API) Admin(method, path, token, body string) *httptest.ResponseRecorder {
	return api.Send(method, path, body, "Authorization", "Bearer "+token)
}

// Send makes a request with headers given as name, value pairs.
func (api *API) Send(method, path, body string, headers ...string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = bytes.NewBufferString(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response := httptest.NewRecorder()
	api.Router.ServeHTTP(response, request)
	return response
}

// SignIn inserts a user holding roles and a session for them, and returns its token.
func SignIn(t *testing.T, roles ...string) string {
	t.Helper()
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	userID, err := q.CreateUser(ctx, db.CreateUserParams{
		Email:         "admin-" + randomHex(6) + "@example.com",
		DisplayName:   "Daw Mi",
		PasswordHash:  "unused",
		Roles:         roles,
		TotpSecretEnc: []byte("sealed"),
	})
	require.NoError(t, err)
	return SessionFor(t, userID)
}

func SessionFor(t *testing.T, userID pgtype.UUID) string {
	t.Helper()
	token, err := tokens.NewSession()
	require.NoError(t, err)
	_, err = db.New(pgtest.Pool(t)).CreateSession(context.Background(), db.CreateSessionParams{
		UserID:    userID,
		TokenHash: tokens.Hash(token),
		Now:       Now,
		ExpiresAt: Now.Add(auth.SessionLifetime),
	})
	require.NoError(t, err)
	return token
}

// JSON asserts response has status and returns its body.
func JSON(t *testing.T, status int, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, status, response.Code, response.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	return body
}

// Refused asserts response is a problem with status and code, and returns it.
func Refused(t *testing.T, status int, code string, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	body := JSON(t, status, response)
	require.Equal(t, code, body["code"])
	require.Equal(t, "application/problem+json", response.Header().Get("Content-Type"))
	return body
}

// liveRedis gives a test a key prefix of its own and deletes its keys afterwards.
func liveRedis(t *testing.T) (*redis.Client, string) {
	rdb := redistest.Client(t)
	prefix := "test-" + randomHex(6) + ":"
	t.Cleanup(func() {
		keys, err := rdb.Keys(context.Background(), prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			rdb.Del(context.Background(), keys...)
		}
	})
	return rdb, prefix
}

func randomHex(size int) string {
	random := make([]byte, size)
	_, _ = rand.Read(random) // crypto/rand.Read never fails
	return hex.EncodeToString(random)
}
