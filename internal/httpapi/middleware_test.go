package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// through serves req through the real middleware chain, in front of the stub
// routes mount registers, and returns the response and every log line.
func through(t *testing.T, mount func(chi.Router), req *http.Request) (*httptest.ResponseRecorder, []map[string]any) {
	t.Helper()
	var logs bytes.Buffer
	res := httptest.NewRecorder()
	router(slog.New(slog.NewJSONHandler(&logs, nil)), nil, mount).ServeHTTP(res, req)
	return res, logLines(t, logs.String())
}

func logLines(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		var line map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &line), l)
		lines = append(lines, line)
	}
	return lines
}

// echo answers with what the middleware put in the request context.
func echo(r chi.Router) {
	r.Get("/echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, RequestID(r.Context())+" "+ClientIP(r.Context()).String())
	})
}

var generatedID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestValidRequestIDIsEchoed(t *testing.T) {
	for _, id := range []string{"a", "abc.DEF_123-x", strings.Repeat("z", 64)} {
		req := httptest.NewRequest(http.MethodGet, "/echo", nil)
		req.Header.Set("X-Request-Id", id)
		res, logs := through(t, echo, req)

		require.Equal(t, id, res.Header().Get("X-Request-Id"))
		require.True(t, strings.HasPrefix(res.Body.String(), id+" "), "context holds the id")
		require.Equal(t, id, logs[0]["request_id"])
	}
}

func TestUnsafeRequestIDIsReplaced(t *testing.T) {
	cases := map[string]string{
		"path":    "../../etc",
		"65":      strings.Repeat("a", 65),
		"missing": "",
		"space":   "two words",
		"newline": "a\nb",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/echo", nil)
			req.Header["X-Request-Id"] = []string{id}
			res, logs := through(t, echo, req)

			got := res.Header().Get("X-Request-Id")
			require.Regexp(t, generatedID, got)
			require.True(t, strings.HasPrefix(res.Body.String(), got+" "), "context holds the new id")
			require.Equal(t, got, logs[0]["request_id"])
		})
	}
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		name   string
		xff    []string
		remote string
		want   string
	}{
		{"last hop is caddy's", []string{"1.1.1.1, 2.2.2.2"}, "10.0.0.1:443", "2.2.2.2"},
		{"last header line", []string{"1.1.1.1", "3.3.3.3,2.2.2.2"}, "10.0.0.1:443", "2.2.2.2"},
		{"ipv6", []string{"1.1.1.1, 2001:db8::1"}, "10.0.0.1:443", "2001:db8::1"},
		{"mapped ipv4", []string{"::ffff:2.2.2.2"}, "10.0.0.1:443", "2.2.2.2"},
		{"no header", nil, "192.0.2.7:51234", "192.0.2.7"},
		{"unparseable header", []string{"1.1.1.1, evil"}, "192.0.2.7:51234", "192.0.2.7"},
		{"ipv6 remote", nil, "[2001:db8::7]:51234", "2001:db8::7"},
		{"nothing parses", []string{"evil"}, "pipe", "invalid IP"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/echo", nil)
			req.RemoteAddr = c.remote
			req.Header["X-Forwarded-For"] = c.xff
			res, _ := through(t, echo, req)
			require.Equal(t, c.want, strings.SplitN(res.Body.String(), " ", 2)[1])
		})
	}
}

func TestOneLogLinePerRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/echo", nil)
	req.Header.Set("X-Request-Id", "req-1")
	res, logs := through(t, echo, req)

	require.Len(t, logs, 1)
	line := logs[0]
	require.Equal(t, "INFO", line["level"])
	require.Equal(t, "request", line["msg"])
	require.Equal(t, "req-1", line["request_id"])
	require.Equal(t, "GET", line["method"])
	require.Equal(t, "/echo", line["route"])
	require.Equal(t, float64(http.StatusOK), line["status"])
	require.Contains(t, line, "duration_ms")
	require.Equal(t, float64(res.Body.Len()), line["bytes"])
}

func TestLogUsesRoutePatternNotPath(t *testing.T) {
	stub := func(r chi.Router) {
		r.Get("/public/manage/{token}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	}
	req := httptest.NewRequest(http.MethodGet, "/public/manage/abc123?email=someone@example.com", nil)
	_, logs := through(t, stub, req)

	require.Len(t, logs, 1)
	require.Equal(t, "/public/manage/{token}", logs[0]["route"])
	require.Equal(t, float64(http.StatusNoContent), logs[0]["status"])
	raw, err := json.Marshal(logs)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "abc123")
	require.NotContains(t, string(raw), "someone")
}

func TestPanicIsA500ProblemAndTheServerKeepsServing(t *testing.T) {
	var logs bytes.Buffer
	stub := func(r chi.Router) {
		r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("nil map") })
		echo(r)
	}
	srv := httptest.NewServer(router(slog.New(slog.NewJSONHandler(&logs, nil)), nil, stub))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/boom")
	require.NoError(t, err)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.NoError(t, res.Body.Close())

	require.Equal(t, http.StatusInternalServerError, res.StatusCode)
	require.Equal(t, "application/problem+json", res.Header.Get("Content-Type"))
	require.Contains(t, string(body), `"code":"internal_error"`)
	require.NotContains(t, string(body), "nil map")
	requireSecurityHeaders(t, res.Header)

	var panicked map[string]any
	for _, line := range logLines(t, logs.String()) {
		if line["msg"] == "panic" {
			panicked = line
		}
	}
	require.NotNil(t, panicked, logs.String())
	require.Equal(t, "ERROR", panicked["level"])
	require.Equal(t, res.Header.Get("X-Request-Id"), panicked["request_id"])
	require.Equal(t, "nil map", panicked["panic"])
	require.Contains(t, panicked["stack"], "middleware_test.go")

	next, err := http.Get(srv.URL + "/echo")
	require.NoError(t, err)
	require.NoError(t, next.Body.Close())
	require.Equal(t, http.StatusOK, next.StatusCode)
}

func TestAbortHandlerIsRepanicked(t *testing.T) {
	h := recoverPanics(quiet)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	require.PanicsWithValue(t, http.ErrAbortHandler, func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	})
}

func requireSecurityHeaders(t *testing.T, h http.Header) {
	t.Helper()
	require.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	require.Equal(t, "no-referrer", h.Get("Referrer-Policy"))
	require.Equal(t, "DENY", h.Get("X-Frame-Options"))
	require.Equal(t, "default-src 'none'; frame-ancestors 'none'", h.Get("Content-Security-Policy"))
	require.Equal(t, "no-store", h.Get("Cache-Control"))
}

func TestSecurityHeadersOnHealthz(t *testing.T) {
	res := httptest.NewRecorder()
	NewRouter(Deps{Log: quiet}).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, res.Code)
	requireSecurityHeaders(t, res.Header())
	require.Regexp(t, generatedID, res.Header().Get("X-Request-Id"))
}

func TestSecurityHeadersOnNotFound(t *testing.T) {
	res := httptest.NewRecorder()
	NewRouter(Deps{Log: quiet}).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/nope", nil))
	require.Equal(t, http.StatusNotFound, res.Code)
	require.Equal(t, "not_found", problemFrom(t, res)["code"])
	requireSecurityHeaders(t, res.Header())
	require.Regexp(t, generatedID, res.Header().Get("X-Request-Id"))
}
