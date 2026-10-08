package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// The limits every route in the API group gets unless it asks for others.
const (
	defaultTimeout = 30 * time.Second
	defaultBodyCap = 64 << 10
)

// timeout is shared by every WithTimeout on one request, so the outermost can
// tell whether the deadline the handler actually ran under was reached.
type timeout struct {
	untimed context.Context // the request before any deadline: ends when the client goes
	handler context.Context // the innermost deadline, the one handlers and queries see
}

type timeoutKey struct{}

// WithTimeout gives the request context a deadline of d, which every query
// inherits. Inside the API group it replaces the 30-second default, longer or
// shorter, so media upload can have 120 seconds. A handler that returns
// without answering once the deadline has passed is answered 503 unavailable.
func WithTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parent := r.Context()
			t, nested := parent.Value(timeoutKey{}).(*timeout)
			if !nested {
				t = &timeout{untimed: parent}
				parent = context.WithValue(parent, timeoutKey{}, t)
			}
			// Detached from any outer deadline so a route can be given longer
			// than its group, then tied back to the client's connection.
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

// cappedBody remembers the body before its cap, so a route's own cap replaces
// its group's instead of sitting behind it.
type cappedBody struct {
	io.ReadCloser
	raw io.ReadCloser
}

// WithBodyCap limits the request body to n bytes. Inside the API group it
// replaces the 64 KiB default, so content saves can have 512 KiB and media
// upload 21 MiB. Reading past the cap fails with *http.MaxBytesError, which
// the strict server answers 413 payload_too_large.
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

// bodyCapByOperation raises the 64 KiB default for the operations that carry
// a whole article in two languages.
var bodyCapByOperation = map[string]int64{
	"createPost": 512 << 10,
	"updatePost": 512 << 10,
}

// timeoutByOperation gives the AI assistant time for the model's answer,
// which its client bounds at 30 seconds, after reading the post.
var timeoutByOperation = map[string]time.Duration{
	"suggestPostVersions": 45 * time.Second,
}

// operationLimits applies bodyCapByOperation and timeoutByOperation to the
// generated routes. It runs just before validation, the first reader of the
// body.
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
