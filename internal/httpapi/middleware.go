package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// Every route in the API group gets these unless it asks for others.
const (
	defaultTimeout = 30 * time.Second
	defaultBodyCap = 64 << 10
)

// Posts carry a whole article in two languages.
var bodyCapByOperation = map[string]int64{
	"createPost": 512 << 10,
	"updatePost": 512 << 10,
}

// The assistant reads the post, then waits up to 30 seconds for the model.
var timeoutByOperation = map[string]time.Duration{
	"suggestPostVersions": 45 * time.Second,
}

type (
	requestIDKey struct{}
	clientIPKey  struct{}
	timeoutKey   struct{}
)

// RequestID is the id of the request ctx belongs to, or "" outside one.
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// ClientIP is the visitor's address for rate limits, or the zero Addr.
func ClientIP(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey{}).(netip.Addr)
	return ip
}

// requestID reuses the caller's X-Request-Id, so the site's logs and ours
// share one id, but only when it is safe to put in a log line.
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

// realIP trusts only the last X-Forwarded-For address: Caddy appends the one
// it saw, and every earlier entry is whatever the client sent.
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

// logRequests logs the route pattern, never the path: management and join
// tokens travel in paths.
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
				"route", routePattern(r),
				"status", status,
				"duration_ms", time.Since(start).Milliseconds(),
				"bytes", rw.bytes)
		})
	}
}

// recoverPanics answers a panic 500 and logs its stack. http.ErrAbortHandler
// is the standard way to abort a response, so it is passed on.
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

// The API only answers JSON, so nothing it sends should be framed, sniffed,
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

// timeout is shared by every WithTimeout on one request, so the outermost can
// tell whether the deadline the handler ran under was reached.
type timeout struct {
	untimed context.Context // ends only when the client goes
	handler context.Context // the innermost deadline, the one handlers see
}

// WithTimeout gives the request a deadline of d, replacing any outer one, and
// answers 503 when the handler returns without answering after it passed.
func WithTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parent := r.Context()
			t, nested := parent.Value(timeoutKey{}).(*timeout)
			if !nested {
				t = &timeout{untimed: parent}
				parent = context.WithValue(parent, timeoutKey{}, t)
			}
			// Detached from the outer deadline so a route can have longer than
			// its group, then tied back to the client's connection.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), d)
			defer cancel()
			defer context.AfterFunc(t.untimed, cancel)()
			t.handler = ctx

			rw := track(w)
			next.ServeHTTP(rw, r.WithContext(ctx))
			if !nested && !rw.started() && errors.Is(t.handler.Err(), context.DeadlineExceeded) {
				writeProblem(rw, apperr.New(apperr.Unavailable, "The request ran out of time."))
			}
		})
	}
}

// cappedBody remembers the uncapped body, so a route's own cap replaces its
// group's instead of sitting behind it.
type cappedBody struct {
	io.ReadCloser
	raw io.ReadCloser
}

// WithBodyCap limits the request body to n bytes, replacing any outer cap.
// Reading past it fails with *http.MaxBytesError, answered 413.
func WithBodyCap(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Body
			if c, ok := raw.(*cappedBody); ok {
				raw = c.raw
			}
			r.Body = &cappedBody{ReadCloser: http.MaxBytesReader(w, raw, n), raw: raw}
			next.ServeHTTP(w, r)
		})
	}
}

// operationLimits applies bodyCapByOperation and timeoutByOperation.
func operationLimits(ops operations) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, _ := ops.lookup(r)
			h := next
			if n, ok := bodyCapByOperation[op.ID]; ok {
				h = WithBodyCap(n)(h)
			}
			if d, ok := timeoutByOperation[op.ID]; ok {
				h = WithTimeout(d)(h)
			}
			h.ServeHTTP(w, r)
		})
	}
}

// recorder notes what was written, for the request log and for middleware
// that must not answer twice.
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
