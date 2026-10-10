package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func load(t *testing.T, file string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromFile(file)
	require.NoError(t, err)
	return spec
}

func index(t *testing.T, spec *openapi3.T) operations {
	t.Helper()
	ops, err := indexOperations(spec)
	require.NoError(t, err)
	return ops
}

// The whole contract: every operation must say
// how it is authenticated, and only the probes and the WebSocket, which has
// its own room ticket, may say "not at all".
func TestEveryOperationDeclaresSecurity(t *testing.T) {
	spec := load(t, "../../openapi.yaml")
	open := map[string]bool{}
	count := 0
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			count++
			scheme, err := securityOf(op)
			require.NoError(t, err, "%s %s (%s)", method, path, op.OperationID)
			if scheme == "" {
				open[op.OperationID] = true
			}
		}
	}
	t.Logf("%d operations checked", count)
	require.Equal(t, map[string]bool{"getHealthz": true, "getReadyz": true, "connectVideoRoom": true}, open)

	sessions := spec.Paths.Find("/auth/sessions").Post
	require.Equal(t, "createSession", sessions.OperationID)
	scheme, err := securityOf(sessions)
	require.NoError(t, err)
	require.Equal(t, schemeServiceKey, scheme, "sign-in takes the service key, not a session")
}

func TestIndexHoldsEachOperationsIDAndSecurity(t *testing.T) {
	require.Equal(t, operations{
		"GET /healthz":               {ID: "getHealthz", Security: schemeServiceKey},
		"GET /readyz":                {ID: "getReadyz", Security: ""},
		"GET /public/stories/{slug}": {ID: "getPublicStory", Security: schemeServiceKey},
		"GET /auth/me":               {ID: "getCurrentUser", Security: schemeSessionToken},
	}, index(t, load(t, "testdata/security.yaml")))
}

// The index is keyed by path template and looked up by chi route pattern; the
// two must be the same strings for every route the real router serves, and
// the ids must be the contract's, not the generated Go names.
func TestIndexKeysAreTheMountedRoutePatterns(t *testing.T) {
	spec, err := gen.GetSpec()
	require.NoError(t, err)
	ops := index(t, spec)

	mounted := map[string]bool{}
	err = chi.Walk(NewRouter(Deps{Log: quiet}).(chi.Routes),
		func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
			// connectVideoRoom and uploadMedia are mounted by hand, outside
			// the generated routes and so outside the embedded spec.
			if route != roomSocketPattern && operationKey(method, route) != operationKey(http.MethodPost, uploadMediaPattern) {
				mounted[operationKey(method, route)] = true
			}
			return nil
		})
	require.NoError(t, err)
	require.NotEmpty(t, mounted)
	for key := range mounted {
		require.Contains(t, ops, key)
	}
	for key := range ops {
		require.Contains(t, mounted, key)
	}
	require.Equal(t, "getHealthz", ops["GET /healthz"].ID)
}

// thingSpec is a one-operation spec whose operation declares security, a
// line of YAML, under a document-wide default it must not inherit.
func thingSpec(t *testing.T, security string) *openapi3.T {
	t.Helper()
	spec, err := openapi3.NewLoader().LoadFromData([]byte(`
openapi: "3.1.0"
info: {title: t, version: "0"}
security: [{serviceKey: []}]
paths:
  /things/{id}:
    get:
      operationId: getThing
      ` + security + `
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      responses: {"200": {description: ok}}
`))
	require.NoError(t, err)
	return spec
}

func TestIndexRejectsUnclearSecurity(t *testing.T) {
	for name, security := range map[string]string{
		"undeclared, despite a document default": "",
		"two schemes together":                   "security: [{serviceKey: [], sessionToken: []}]",
		"either of two schemes":                  "security: [{serviceKey: []}, {sessionToken: []}]",
		"an empty requirement":                   "security: [{}]",
		"an unknown scheme":                      "security: [{apiKey: []}]",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := indexOperations(thingSpec(t, security))
			require.ErrorIs(t, err, errUnclearSecurity)
			require.ErrorContains(t, err, "GET /things/{id}")
		})
	}
}

func TestMountRefusesUnclearSecurity(t *testing.T) {
	require.Panics(t, func() {
		mountAPI(&server{}, thingSpec(t, ""), Deps{ServiceKey: testServiceKey, RateLimits: noRedis, Log: quiet})
	})
	require.NotPanics(t, func() {
		mountAPI(&server{}, thingSpec(t, "security: [{serviceKey: []}]"), Deps{ServiceKey: testServiceKey, RateLimits: noRedis, Log: quiet})
	})
}

const (
	testServiceKey = "zz-service-key-0123456789-abcdefgh"
	// nextServerIP is where httptest requests come from: in production, the Next server.
	nextServerIP = "192.0.2.1"
	visitorIP    = "203.0.113.9"
)

// withKey serves req through the real middleware chain to stub routes for
// the security fixture's operations, plus one the fixture lacks, all behind
// requireServiceKey(key). Each route answers with the client IP it sees. It
// returns the response and the raw log.
func withKey(t *testing.T, key string, req *http.Request) (*httptest.ResponseRecorder, string) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	check := requireServiceKey(key, index(t, load(t, "testdata/security.yaml")), log)
	answerIP := func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ClientIP(r.Context()).String())
	}
	mount := func(r chi.Router) {
		r = r.With(check)
		for _, path := range []string{"/public/stories/{slug}", "/readyz", "/auth/me", "/unindexed"} {
			r.Get(path, answerIP)
		}
	}
	res := httptest.NewRecorder()
	router(log, nil, mount).ServeHTTP(res, req)
	return res, logs.String()
}

// getWith builds a request to path with headers, given as name, value pairs.
func getWith(path string, headers ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	return req
}

func requireUnauthenticated(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, res.Code, res.Body.String())
	require.Equal(t, "unauthenticated", problemFrom(t, res)["code"])
}

func TestValidKeyReachesTheHandlerWithTheVisitorIP(t *testing.T) {
	for sent, want := range map[string]string{
		visitorIP:             visitorIP,
		"::ffff:" + visitorIP: visitorIP,
		"2001:db8::9":         "2001:db8::9",
	} {
		res, _ := withKey(t, testServiceKey, getWith("/public/stories/first-session",
			"X-Service-Key", testServiceKey, "X-Visitor-IP", sent))
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Equal(t, want, res.Body.String())
	}
}

func TestPublicRouteWithoutKeyIs401(t *testing.T) {
	t.Run("no header", func(t *testing.T) {
		res, _ := withKey(t, testServiceKey, getWith("/public/stories/first-session"))
		requireUnauthenticated(t, res)
	})
	t.Run("empty header", func(t *testing.T) {
		req := getWith("/public/stories/first-session")
		req.Header["X-Service-Key"] = []string{""}
		res, _ := withKey(t, testServiceKey, req)
		requireUnauthenticated(t, res)
	})
	t.Run("server started without a key", func(t *testing.T) {
		res, _ := withKey(t, "", getWith("/public/stories/first-session"))
		requireUnauthenticated(t, res)
	})
}

func TestWrongKeyIs401(t *testing.T) {
	for name, key := range map[string]string{
		"same length": strings.Replace(testServiceKey, "0123", "0124", 1),
		"prefix":      testServiceKey[:len(testServiceKey)-1],
		"extended":    testServiceKey + "x",
		"other case":  strings.ToUpper(testServiceKey),
	} {
		t.Run(name, func(t *testing.T) {
			res, _ := withKey(t, testServiceKey, getWith("/public/stories/first-session", "X-Service-Key", key))
			requireUnauthenticated(t, res)
		})
	}
}

func TestVisitorIPWithoutAValidKeyIsIgnored(t *testing.T) {
	wrong := strings.Replace(testServiceKey, "0123", "0124", 1)

	res, _ := withKey(t, testServiceKey, getWith("/public/stories/first-session",
		"X-Service-Key", wrong, "X-Visitor-IP", visitorIP))
	requireUnauthenticated(t, res)

	// security: [] checks no key, so it has none to trust the header with.
	res, _ = withKey(t, testServiceKey, getWith("/readyz",
		"X-Service-Key", wrong, "X-Visitor-IP", visitorIP))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, nextServerIP, res.Body.String())
}

func TestMissingOrInvalidVisitorIPFallsBackAndIsLogged(t *testing.T) {
	for name, sent := range map[string][]string{
		"missing": nil,
		"invalid": {"zz-not-an-ip-zz"},
		"empty":   {""},
	} {
		t.Run(name, func(t *testing.T) {
			req := getWith("/public/stories/first-session", "X-Service-Key", testServiceKey)
			req.Header["X-Visitor-IP"] = sent
			res, raw := withKey(t, testServiceKey, req)

			require.Equal(t, http.StatusOK, res.Code, res.Body.String())
			require.Equal(t, nextServerIP, res.Body.String())
			logs := logLines(t, raw)
			require.Len(t, logs, 2)
			require.Equal(t, "WARN", logs[0]["level"])
			require.Equal(t, "visitor_ip_missing", logs[0]["msg"])
			require.Equal(t, "/public/stories/{slug}", logs[0]["route"])
			require.NotContains(t, raw, "zz-not-an-ip-zz")
			require.NotContains(t, raw, "first-session")
		})
	}
}

func TestServiceKeyIsNeverLogged(t *testing.T) {
	wrong := strings.Replace(testServiceKey, "0123", "0124", 1)
	for name, sent := range map[string]string{"wrong key": wrong, "valid key": testServiceKey} {
		t.Run(name, func(t *testing.T) {
			res, raw := withKey(t, testServiceKey, getWith("/public/stories/first-session", "X-Service-Key", sent))
			require.NotEmpty(t, raw)
			for _, secret := range []string{testServiceKey, wrong} {
				require.NotContains(t, raw, secret)
				require.NotContains(t, res.Body.String(), secret)
			}
		})
	}
}

func TestOpenOperationsNeedNoKey(t *testing.T) {
	res, _ := withKey(t, testServiceKey, getWith("/readyz"))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, nextServerIP, res.Body.String())
}

// Signed-in calls carry a session, not the service key; their check is the
// session middleware's.
func TestSessionOperationsAreLeftToTheirOwnCheck(t *testing.T) {
	res, _ := withKey(t, testServiceKey, getWith("/auth/me", "X-Visitor-IP", visitorIP))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	require.Equal(t, nextServerIP, res.Body.String())
}

func TestRouteMissingFromTheIndexIs500(t *testing.T) {
	res, raw := withKey(t, testServiceKey, getWith("/unindexed", "X-Service-Key", testServiceKey))
	require.Equal(t, http.StatusInternalServerError, res.Code, res.Body.String())
	require.Equal(t, "internal_error", problemFrom(t, res)["code"])
	require.Equal(t, "operation not indexed", logLines(t, raw)[0]["msg"])
}

// mountAPI puts the key check in front of validation: a caller without the
// key is refused before the contract says anything about its request.
func TestServiceKeyIsCheckedBeforeValidation(t *testing.T) {
	mount := mountAPI(&server{}, load(t, "testdata/security.yaml"), Deps{ServiceKey: testServiceKey, RateLimits: noRedis, Log: quiet})

	res, _ := through(t, mount, getWith("/healthz"))
	requireUnauthenticated(t, res)

	res, _ = through(t, mount, getWith("/healthz", "X-Service-Key", testServiceKey))
	require.Equal(t, []map[string]any{fieldError("probe", "is required")}, invalid(t, res))

	res, _ = through(t, mount, getWith("/healthz?probe=yes", "X-Service-Key", testServiceKey))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
}
