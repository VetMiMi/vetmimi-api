package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

type (
	requestIDKey struct{}
	clientIPKey  struct{}
)

// RequestID is the id of the request ctx belongs to, or "" outside one.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ClientIP is the visitor's address, for rate limits. It is the zero Addr
// when the request carried none that parses.
func ClientIP(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return ip
}

// requestID reuses the caller's X-Request-Id, so the site's logs and ours
// share one id, but only when it is short and plain enough to be safe in a
// log line; anything else is replaced.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if !validRequestID(id) {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

func validRequestID(id string) bool {
	if len(id) < 1 || len(id) > 64 {
		return false
	}
	for _, c := range []byte(id) {
		ok := 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
			c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// realIP trusts only the last X-Forwarded-For address: the API port is
// reachable only through Caddy, which appends the address it saw, and every
// earlier entry is whatever the client chose to send.
func realIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), clientIPKey{}, clientAddr(r))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func clientAddr(r *http.Request) netip.Addr {
	if lines := r.Header.Values("X-Forwarded-For"); len(lines) > 0 {
		hops := strings.Split(lines[len(lines)-1], ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
			return ip.Unmap()
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// logRequests writes one line per request once it completes. It logs the
// route pattern, never the path or query, because management and join tokens
// travel in paths.
func logRequests(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := track(w)
			next.ServeHTTP(rw, r)

			status := rw.status
			if status == 0 {
				status = http.StatusOK
			}
			log.InfoContext(r.Context(), "request",
				"request_id", RequestID(r.Context()),
				"method", r.Method,
				"route", chi.RouteContext(r.Context()).RoutePattern(),
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rw.bytes)
		})
	}
}

// recoverPanics answers a panic as 500 internal_error and logs its stack, so
// one bad request cannot take the process down. http.ErrAbortHandler is the
// standard way to abort a response and is passed on to net/http.
func recoverPanics(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := track(w)
			defer func() {
				p := recover()
				if p == nil {
					return
				}
				if p == http.ErrAbortHandler {
					panic(p)
				}
				log.ErrorContext(r.Context(), "panic",
					"request_id", RequestID(r.Context()),
					"panic", fmt.Sprint(p),
					"stack", string(debug.Stack()))
				if !rw.started() {
					writeProblem(rw, apperr.New(apperr.InternalError, "Unexpected failure."))
				}
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// securityHeaders apply to every response, errors and 404s included. The API
// only ever answers JSON, so nothing it sends should be framed, sniffed,
// cached or allowed to load anything.
var securityHeaders = map[string]string{
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
	"X-Frame-Options":         "DENY",
	"Content-Security-Policy": "default-src 'none'; frame-ancestors 'none'",
	"Cache-Control":           "no-store",
}

func setSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range securityHeaders {
			w.Header().Set(k, v)
		}
		next.ServeHTTP(w, r)
	})
}

// recorder notes what was written, for the request log and for the
// middleware that must not answer a request a second time.
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

// track returns w as a recorder, reusing the one an outer middleware made.
func track(w http.ResponseWriter) *recorder {
	if rw, ok := w.(*recorder); ok {
		return rw
	}
	return &recorder{ResponseWriter: w}
}

func (rw *recorder) WriteHeader(status int) {
	if rw.status == 0 {
		rw.status = status
	}
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *recorder) Write(b []byte) (int, error) {
	if rw.status == 0 {
		rw.status = http.StatusOK
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach Flush and Hijack underneath.
func (rw *recorder) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

func (rw *recorder) started() bool { return rw.status != 0 }
