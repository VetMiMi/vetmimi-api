package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

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
	router(log, mount).ServeHTTP(res, req)
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
	mount := mountAPI(&server{}, load(t, "testdata/security.yaml"), testServiceKey, quiet)

	res, _ := through(t, mount, getWith("/healthz"))
	requireUnauthenticated(t, res)

	res, _ = through(t, mount, getWith("/healthz", "X-Service-Key", testServiceKey))
	require.Equal(t, []map[string]any{fieldError("probe", "is required")}, invalid(t, res))

	res, _ = through(t, mount, getWith("/healthz?probe=yes", "X-Service-Key", testServiceKey))
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
}
