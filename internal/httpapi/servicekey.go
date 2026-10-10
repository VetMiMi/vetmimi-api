package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"net/netip"

	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// requireServiceKey answers 401 unauthenticated to a request for a serviceKey
// operation that does not carry key in X-Service-Key (ADR-002). Once the key
// is good, X-Visitor-IP names the visitor, because the address the request
// came from is the Next server's. Operations declaring security: [] need
// nothing, and sessionToken operations are checked by their own middleware.
// Neither the key nor any header value is ever logged.
func requireServiceKey(key string, ops operations, log *slog.Logger) gen.MiddlewareFunc {
	// Comparing digests rather than the keys keeps the comparison from
	// leaking the key's length: ConstantTimeCompare returns at once on
	// inputs of different lengths.
	want := sha256.Sum256([]byte(key))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := ops.lookup(r)
			if !ok {
				// The index is built from the spec the routes come from,
				// so a route missing from it is a bug, and refused.
				log.ErrorContext(r.Context(), "operation not indexed",
					"request_id", RequestID(r.Context()), "route", routePattern(r))
				writeProblem(w, apperr.New(apperr.InternalError, "Unexpected failure."))
				return
			}
			if op.Security != schemeServiceKey {
				next.ServeHTTP(w, r)
				return
			}
			if !validServiceKey(r.Header.Get("X-Service-Key"), want) {
				writeProblem(w, apperr.New(apperr.Unauthenticated, "A valid service key is required."))
				return
			}
			next.ServeHTTP(w, r.WithContext(withVisitorIP(r, log)))
		})
	}
}

// validServiceKey refuses an empty key outright, so a server that somehow
// started without one refuses every public call instead of accepting callers
// that send none.
func validServiceKey(got string, want [sha256.Size]byte) bool {
	sum := sha256.Sum256([]byte(got))
	return got != "" && subtle.ConstantTimeCompare(sum[:], want[:]) == 1
}

// withVisitorIP makes X-Visitor-IP the client IP. Without it, rate limits
// would count every visitor as the Next server, so a missing or unparseable
// header is logged, by route only, and the real IP stays in place.
func withVisitorIP(r *http.Request, log *slog.Logger) context.Context {
	ip, err := netip.ParseAddr(r.Header.Get("X-Visitor-IP"))
	if err != nil {
		log.WarnContext(r.Context(), "visitor_ip_missing",
			"request_id", RequestID(r.Context()), "route", routePattern(r))
		return r.Context()
	}
	return context.WithValue(r.Context(), clientIPKey{}, ip.Unmap())
}

func routePattern(r *http.Request) string {
	return chi.RouteContext(r.Context()).RoutePattern()
}
