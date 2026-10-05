package httpapi

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
)

// The security schemes openapi.yaml declares. An operation requires exactly
// one of them, or declares security: [] and requires nothing.
const (
	schemeServiceKey   = "serviceKey"
	schemeSessionToken = "sessionToken"
)

// operation is what the per-operation middlewares need to know about the
// route a request matched.
type operation struct {
	ID string
	// Security is the one scheme the operation requires, or "" when it
	// declares security: [].
	Security string
}

// operations indexes a spec's operations by method and path template. A path
// template is exactly the chi route pattern the generated router registers,
// so a middleware finds its operation from the matched route alone, and a new
// route is guarded by what its contract declares rather than by its path.
type operations map[string]operation

func operationKey(method, pattern string) string { return method + " " + pattern }

// indexOperations reads every operation in spec once, at start-up. It refuses
// an operation whose security is not one plain requirement, so nothing is
// served under a rule the middlewares do not understand.
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

// securityOf names the one scheme op requires. The operation must declare it
// itself: a document-wide default is not read, so no operation becomes
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

// lookup finds the operation chi routed r to. It only answers inside a route,
// once chi has matched the pattern.
func (ops operations) lookup(r *http.Request) (operation, bool) {
	op, ok := ops[operationKey(r.Method, routePattern(r))]
	return op, ok
}
