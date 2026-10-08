package httpapi

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// noRedis counts nothing, as if Redis were down, for tests not about limits.
var noRedis = NewRateLimits(nil, quiet, time.Now)

const (
	otherVisitorIP = "203.0.113.10"
	manageToken    = "zz-manage-token-0123456789-abcdefghijklmnop"
	joinToken      = "zz-join-token-0123456789-abcdefghijklmnopqr"
	signInURL      = "/healthz?probe=yes"
	meURL          = "/auth/me?probe=yes"
)

// fakeClock is a clock a test moves by hand.
type fakeClock struct{ at time.Time }

func (c *fakeClock) now() time.Time { return c.at }

// limitedAPI serves a fixture spec through the real middlewares,
// counting in Redis under a key prefix of the test's own, with sessions on
// the test database.
type limitedAPI struct {
	handler http.Handler
	clock   *fakeClock
	logs    *bytes.Buffer
	rdb     *redis.Client
	prefix  string
}

// newLimitedAPI counts in rdb, or in nothing when rdb is nil. Sign-in,
// /readyz and /auth/me are the generated routes, mounted by mountAPI; the
// others are stubs behind the same key check and limiter.
func newLimitedAPI(t *testing.T, rdb *redis.Client, prefix string) *limitedAPI {
	t.Helper()
	spec := load(t, "testdata/ratelimit.yaml")
	ops := index(t, spec)
	answer := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	return serveSpec(t, rdb, prefix, spec, func(r chi.Router, deps Deps) {
		stubs := r.With(requireServiceKey(testServiceKey, ops, deps.Log), deps.RateLimits.operations(ops))
		stubs.Get("/public/manage/{token}", answer)
		stubs.Post("/public/manage/{token}/cancel", answer)
		stubs.Get("/public/sessions/{token}", answer)
		stubs.Get("/public/stories/{slug}", answer)
	})
}

// serveSpec serves the generated routes through mountAPI, indexed by spec,
// counting in rdb as newLimitedAPI does, with whatever stubs mounts beside
// them.
func serveSpec(t *testing.T, rdb *redis.Client, prefix string, spec *openapi3.T, stubs func(chi.Router, Deps)) *limitedAPI {
	t.Helper()
	a := &limitedAPI{
		clock:  &fakeClock{at: time.Date(2026, 10, 5, 9, 30, 15, 0, time.UTC)},
		logs:   &bytes.Buffer{},
		rdb:    rdb,
		prefix: prefix,
	}
	log := slog.New(slog.NewJSONHandler(a.logs, nil))
	var limiter *platform.Limiter
	if rdb != nil {
		limiter = platform.NewLimiter(rdb, prefix, a.clock.now)
	}
	deps := Deps{
		PingPostgres: ok,
		PingRedis:    ok,
		Log:          log,
		ServiceKey:   testServiceKey,
		RateLimits:   NewRateLimits(limiter, log, a.clock.now),
		Sessions:     newSessions(t, a.clock.now),
	}
	a.handler = router(log, nil, func(r chi.Router) {
		mountAPI(&server{deps}, spec, deps)(r)
		if stubs != nil {
			stubs(r, deps)
		}
	})
	return a
}

// liveAPI counts in REDIS_URL_TEST and deletes its keys when the test ends.
func liveAPI(t *testing.T) *limitedAPI {
	t.Helper()
	rdb, prefix := liveRedis(t)
	return newLimitedAPI(t, rdb, prefix)
}

// liveRedis opens REDIS_URL_TEST, failing rather than skipping without it,
// and returns a key prefix of the test's own, whose keys it deletes when the
// test ends; the database is never flushed.
func liveRedis(t *testing.T) (*redis.Client, string) {
	t.Helper()
	url := os.Getenv("REDIS_URL_TEST")
	if url == "" {
		t.Fatal("REDIS_URL_TEST is not set; point it at a Redis database tests may write to, " +
			"for example redis://localhost:6379/1")
	}
	rdb, err := platform.OpenRedis(url)
	require.NoError(t, err)
	t.Cleanup(func() { rdb.Close() })
	random := make([]byte, 6)
	_, err = rand.Read(random)
	require.NoError(t, err)
	prefix := "test-" + hex.EncodeToString(random) + ":"
	t.Cleanup(func() {
		if keys := keysWithPrefix(t, rdb, prefix); len(keys) > 0 {
			require.NoError(t, rdb.Del(context.Background(), keys...).Err())
		}
	})
	return rdb, prefix
}

func keysWithPrefix(t *testing.T, rdb *redis.Client, prefix string) []string {
	t.Helper()
	ctx := context.Background()
	var keys []string
	iter := rdb.Scan(ctx, 0, prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	require.NoError(t, iter.Err())
	return keys
}

func (a *limitedAPI) keys(t *testing.T) []string {
	t.Helper()
	return keysWithPrefix(t, a.rdb, a.prefix)
}

// send serves a request with headers, given as name, value pairs.
func (a *limitedAPI) send(method, path string, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

func (a *limitedAPI) signIn(ip string) *httptest.ResponseRecorder {
	return a.send(http.MethodGet, signInURL, "X-Service-Key", testServiceKey, "X-Visitor-IP", ip)
}

func (a *limitedAPI) unavailableLogs(t *testing.T) []map[string]any {
	t.Helper()
	var found []map[string]any
	for _, line := range logLines(t, a.logs.String()) {
		if line["msg"] == "rate_limit_unavailable" {
			found = append(found, line)
		}
	}
	return found
}

func requireRateLimited(t *testing.T, res *httptest.ResponseRecorder) int {
	t.Helper()
	require.Equal(t, http.StatusTooManyRequests, res.Code, res.Body.String())
	require.Equal(t, "rate_limited", problemFrom(t, res)["code"])
	wait, err := strconv.Atoi(res.Header().Get("Retry-After"))
	require.NoError(t, err)
	return wait
}

func TestFiveSignInsFromOneIPPassAndAnotherIPStillPasses(t *testing.T) {
	a := liveAPI(t)
	for i := range 5 {
		res := a.signIn(visitorIP)
		require.Equal(t, http.StatusOK, res.Code, "attempt %d: %s", i+1, res.Body.String())
	}
	res := a.signIn(otherVisitorIP)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
}

func TestSixthSignInFromOneIPIs429(t *testing.T) {
	a := liveAPI(t)
	for range 5 {
		require.Equal(t, http.StatusOK, a.signIn(visitorIP).Code)
	}
	wait := requireRateLimited(t, a.signIn(visitorIP))
	require.GreaterOrEqual(t, wait, 1)
	require.LessOrEqual(t, wait, 60)
	require.Equal(t, 45, wait, "09:30:15 waits for the window starting 09:31:00")
}

// Each group allows its own number of requests per subject, then answers 429.
func TestEachGroupAllowsItsLimitThen429(t *testing.T) {
	key := func(*testing.T, *limitedAPI) []string {
		return []string{"X-Service-Key", testServiceKey, "X-Visitor-IP", visitorIP}
	}
	signedIn := func(t *testing.T, a *limitedAPI) []string { return bearer(a.newSession(t)) }
	for name, tc := range map[string]struct {
		method, path string
		limit        int
		headers      func(*testing.T, *limitedAPI) []string
	}{
		"availability": {http.MethodGet, "/readyz", 60, key},
		"manage link":  {http.MethodGet, "/public/manage/" + manageToken, 20, key},
		"video join":   {http.MethodGet, "/public/sessions/" + joinToken, 30, key},
		"other public": {http.MethodGet, "/public/stories/first-session", 1200, key},
		"signed in":    {http.MethodGet, meURL, 300, signedIn},
	} {
		t.Run(name, func(t *testing.T) {
			a := liveAPI(t)
			headers := tc.headers(t, a)
			for i := range tc.limit {
				res := a.send(tc.method, tc.path, headers...)
				require.Equal(t, http.StatusOK, res.Code, "request %d: %s", i+1, res.Body.String())
			}
			requireRateLimited(t, a.send(tc.method, tc.path, headers...))
		})
	}
}

func TestManageLinkIsCountedPerTokenAcrossItsOperations(t *testing.T) {
	a := liveAPI(t)
	key := []string{"X-Service-Key", testServiceKey}
	for i := range 20 {
		method, path := http.MethodGet, "/public/manage/"+manageToken
		if i%2 == 1 {
			method, path = http.MethodPost, path+"/cancel"
		}
		require.Equal(t, http.StatusOK, a.send(method, path, key...).Code, "request %d", i+1)
	}
	wait := requireRateLimited(t, a.send(http.MethodPost, "/public/manage/"+manageToken+"/cancel", key...))
	require.Equal(t, 29*60+45, wait, "an hour's window: 09:30:15 waits for 10:00")

	res := a.send(http.MethodGet, "/public/manage/other-"+manageToken, key...)
	require.Equal(t, http.StatusOK, res.Code, "another link has its own count")
}

func TestRoomUpgradeIsLimitedPerRoom(t *testing.T) {
	a := liveAPI(t)
	limits := NewRateLimits(platform.NewLimiter(a.rdb, a.prefix, a.clock.now), quiet, a.clock.now)
	upgrade := func(room string) *httptest.ResponseRecorder {
		res := httptest.NewRecorder()
		if limits.AllowRoomUpgrade(res, httptest.NewRequest(http.MethodGet, "/video/rooms/x/ws", nil), room) {
			res.WriteHeader(http.StatusSwitchingProtocols)
		}
		return res
	}
	for i := range 20 {
		require.Equal(t, http.StatusSwitchingProtocols, upgrade("room-a").Code, "upgrade %d", i+1)
	}
	requireRateLimited(t, upgrade("room-a"))
	require.Equal(t, http.StatusSwitchingProtocols, upgrade("room-b").Code)
}

// Redis keeps hashes: no address, token or key a request was counted by.
func TestRedisKeysHoldNoAddressTokenOrKey(t *testing.T) {
	a := liveAPI(t)
	key := []string{"X-Service-Key", testServiceKey, "X-Visitor-IP", visitorIP}
	a.signIn(visitorIP)
	a.send(http.MethodGet, "/public/manage/"+manageToken, key...)
	a.send(http.MethodGet, "/public/sessions/"+joinToken, key...)
	a.send(http.MethodGet, "/public/stories/first-session", key...)
	sessionToken := a.newSession(t)
	a.send(http.MethodGet, meURL, bearer(sessionToken)...)

	keys := a.keys(t)
	shape := regexp.MustCompile(`^` + regexp.QuoteMeta(a.prefix) +
		`rl:(sign-in|manage|video-session|service-key|session):[0-9a-f]{32}:[0-9]+$`)
	require.Len(t, keys, 5)
	for _, k := range keys {
		require.Regexp(t, shape, k)
		for _, raw := range []string{visitorIP, manageToken, joinToken, testServiceKey, sessionToken} {
			require.NotContains(t, k, raw)
		}
	}
}

// Losing Redis must not stop bookings (ADR-006), but sign-in without its
// limit would help an attacker, so it alone answers 503. Each request waits
// at most the limiter's short timeout.
func TestRedisDownLetsPublicCallsThroughAndSignInIs503(t *testing.T) {
	rdb, err := platform.OpenRedis("redis://127.0.0.1:1/0")
	require.NoError(t, err)
	defer rdb.Close()
	a := newLimitedAPI(t, rdb, "")
	key := []string{"X-Service-Key", testServiceKey, "X-Visitor-IP", visitorIP}

	timed := func(method, path string, headers ...string) *httptest.ResponseRecorder {
		began := time.Now()
		res := a.send(method, path, headers...)
		elapsed := time.Since(began)
		t.Logf("%s %s with Redis stopped answered %d after %s", method, path, res.Code, elapsed)
		require.Less(t, elapsed, 400*time.Millisecond)
		return res
	}
	require.Equal(t, http.StatusOK, timed(http.MethodGet, "/readyz", key...).Code)
	require.Equal(t, http.StatusOK, timed(http.MethodGet, "/public/manage/"+manageToken, key...).Code)
	require.Equal(t, http.StatusOK, timed(http.MethodGet, meURL, bearer(a.newSession(t))...).Code)

	res := timed(http.MethodGet, signInURL, key...)
	require.Equal(t, http.StatusServiceUnavailable, res.Code, res.Body.String())
	require.Equal(t, "unavailable", problemFrom(t, res)["code"])

	logs := a.unavailableLogs(t)
	require.Len(t, logs, 1, "four requests in one minute, one line")
	require.Equal(t, "ERROR", logs[0]["level"])
	require.Equal(t, "availability", logs[0]["group"])
	require.NotContains(t, a.logs.String(), visitorIP)
	require.NotContains(t, a.logs.String(), manageToken)
}

func TestRateLimitUnavailableIsLoggedAtMostOnceAMinute(t *testing.T) {
	a := newLimitedAPI(t, nil, "")
	public := func() {
		res := a.send(http.MethodGet, "/readyz", "X-Service-Key", testServiceKey)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	}
	public()
	public()
	require.Len(t, a.unavailableLogs(t), 1)

	a.clock.at = a.clock.at.Add(time.Minute - time.Nanosecond)
	public()
	require.Len(t, a.unavailableLogs(t), 1, "still inside the minute")

	a.clock.at = a.clock.at.Add(time.Nanosecond)
	public()
	public()
	require.Len(t, a.unavailableLogs(t), 2, "a minute on, once more")
}

// contractOperations indexes openapi.yaml by operation id.
func contractOperations(t *testing.T) map[string]operation {
	t.Helper()
	byID := map[string]operation{}
	for _, op := range index(t, load(t, "../../openapi.yaml")) {
		byID[op.ID] = op
	}
	return byID
}

// Every operation in the contract is limited, except the three declaring
// security: []: the probes, and the WebSocket, which calls AllowRoomUpgrade.
func TestEveryOperationHasALimitGroup(t *testing.T) {
	unlimited := map[string]bool{}
	for id, op := range contractOperations(t) {
		if _, ok := limitFor(op); !ok {
			unlimited[id] = true
		}
	}
	require.Equal(t, map[string]bool{"getHealthz": true, "getReadyz": true, "connectVideoRoom": true}, unlimited)
}

// The table in docs/architecture.md, "Security → Rate limits". Each operation
// named in limitsByOperation must exist, or a misspelt id would leave it on
// the default, and must require the service key, which is what vouches for
// the visitor IP; one counted per user must require a session, which names
// the user.
func TestLimitsMatchTheArchitecture(t *testing.T) {
	type want struct {
		key    keyBy
		limit  int
		window time.Duration
	}
	ops := contractOperations(t)
	for id, l := range limitsByOperation {
		require.Contains(t, ops, id)
		scheme := schemeServiceKey
		if l.key == byUser {
			scheme = schemeSessionToken
		}
		require.Equal(t, scheme, ops[id].Security, id)
	}
	for id, w := range map[string]want{
		"createSession":              {byVisitorIP, 5, time.Minute},
		"createPublicAppointment":    {byVisitorIP, 5, 10 * time.Minute},
		"createContactEnquiry":       {byVisitorIP, 5, time.Hour},
		"getPublicAvailability":      {byVisitorIP, 60, time.Minute},
		"getManagedAppointment":      {byPathToken, 20, time.Hour},
		"cancelManagedAppointment":   {byPathToken, 20, time.Hour},
		"requestManagedReschedule":   {byPathToken, 20, time.Hour},
		"getPublicSession":           {byPathToken, 30, time.Minute},
		"createRoomTicket":           {byPathToken, 30, time.Minute},
		"listPublicBookableServices": {byServiceKey, 1200, time.Minute},
		"getCurrentUser":             {bySessionToken, 300, time.Minute},
		"confirmAppointment":         {bySessionToken, 300, time.Minute},
		"suggestPostVersions":        {byUser, 20, time.Hour},
	} {
		l, ok := limitFor(ops[id])
		require.True(t, ok, id)
		require.Equal(t, w, want{l.key, l.limit, l.window}, id)
		require.Equal(t, id == "createSession", l.failClosed, "%s: only sign-in fails closed", id)
	}
	require.Equal(t, rateLimit{"room", byRoomID, 20, time.Minute, false}, roomUpgrade)
}
