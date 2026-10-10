package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// An operation requires exactly one of these schemes, or declares security: [].
const (
	schemeServiceKey   = "serviceKey"
	schemeSessionToken = "sessionToken"
)

type operation struct {
	ID       string
	Security string // "" when the operation declares security: []
}

// operations indexes the spec's operations by method and path template, which
// is exactly the chi route pattern, so each check finds its operation from
// the matched route and follows what the contract declares.
type operations map[string]operation

func operationKey(method, pattern string) string { return method + " " + pattern }

// indexOperations refuses an operation whose security the checks would not understand.
func indexOperations(spec *openapi3.T) (operations, error) {
	ops := operations{}
	var errs []error
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			scheme, err := securityOf(op)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s %s: %w", method, path, err))
				continue
			}
			ops[operationKey(method, path)] = operation{ID: op.OperationID, Security: scheme}
		}
	}
	return ops, errors.Join(errs...)
}

var errUnclearSecurity = errors.New(
	"must declare its own security: exactly one of serviceKey or sessionToken, or []")

// securityOf ignores the document-wide default, so no operation becomes
// public, or private, by omission.
func securityOf(op *openapi3.Operation) (string, error) {
	if op.Security == nil {
		return "", errUnclearSecurity
	}
	reqs := *op.Security
	if len(reqs) == 0 {
		return "", nil
	}
	if len(reqs) == 1 && len(reqs[0]) == 1 {
		for scheme := range reqs[0] {
			if scheme == schemeServiceKey || scheme == schemeSessionToken {
				return scheme, nil
			}
		}
	}
	return "", errUnclearSecurity
}

// lookup finds the operation chi routed r to; it only works inside a route.
func (ops operations) lookup(r *http.Request) (operation, bool) {
	op, ok := ops[operationKey(r.Method, routePattern(r))]
	return op, ok
}

func routePattern(r *http.Request) string {
	return chi.RouteContext(r.Context()).RoutePattern()
}

// requireServiceKey answers 401 to a serviceKey operation without the key in
// X-Service-Key. Once the key is good, X-Visitor-IP names the visitor, because
// the request itself comes from the Next server. The key is never logged.
func requireServiceKey(key string, ops operations, log *slog.Logger) gen.MiddlewareFunc {
	// Comparing digests keeps the comparison from leaking the key's length.
	want := sha256.Sum256([]byte(key))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, ok := ops.lookup(r)
			if !ok {
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

// validServiceKey refuses an empty key, so a server started without one
// refuses every public call.
func validServiceKey(got string, want [sha256.Size]byte) bool {
	sum := sha256.Sum256([]byte(got))
	return got != "" && subtle.ConstantTimeCompare(sum[:], want[:]) == 1
}

// withVisitorIP makes X-Visitor-IP the client IP. A missing header would
// count every visitor as the Next server, so it is logged.
func withVisitorIP(r *http.Request, log *slog.Logger) context.Context {
	ip, err := netip.ParseAddr(r.Header.Get("X-Visitor-IP"))
	if err != nil {
		log.WarnContext(r.Context(), "visitor_ip_missing",
			"request_id", RequestID(r.Context()), "route", routePattern(r))
		return r.Context()
	}
	return context.WithValue(r.Context(), clientIPKey{}, ip.Unmap())
}

// requireSession answers 401 to a sessionToken operation without a live
// session as its bearer token, and puts the session in the context for
// auth.FromContext. The token is never logged.
func requireSession(sessions *auth.Sessions, ops operations, log *slog.Logger) gen.MiddlewareFunc {
	fail := responseError(log)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if op, _ := ops.lookup(r); op.Security != schemeSessionToken {
				next.ServeHTTP(w, r)
				return
			}
			if sessions == nil {
				writeProblem(w, apperr.New(apperr.Unauthenticated, "A valid session is required."))
				return
			}
			session, err := sessions.Authenticate(r.Context(), bearerToken(r))
			if err != nil {
				fail(w, r, err)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithSession(r.Context(), session)))
		})
	}
}

// bearerToken reads "Authorization: Bearer <token>"; the scheme is case-insensitive.
func bearerToken(r *http.Request) string {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}
