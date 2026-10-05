package httpapi

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// NewRouter returns the whole HTTP API: every route in openapi.yaml behind
// the middleware chain in docs/architecture.md, "Request lifecycle". Every
// error it answers, unknown routes included, is an application/problem+json
// body.
func NewRouter(deps Deps) http.Handler {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	spec, err := gen.GetSpec()
	if err != nil {
		// The spec is embedded at build time and generate-check keeps it in
		// step with the code, so failing to decode it is a broken build.
		panic(fmt.Sprintf("httpapi: decode the embedded OpenAPI spec: %v", err))
	}
	return router(log, mountAPI(&server{deps}, spec, deps.ServiceKey, log))
}

// router builds the chain around the routes mount registers, which tests use
// to put stub routes behind the real middleware.
func router(log *slog.Logger, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID, realIP, logRequests(log), recoverPanics(log), setSecurityHeaders)
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)

	// Routes that must outlive the request timeout and body cap mount here,
	// outside the group below: the video WebSocket (ADR-007).

	r.Group(func(r chi.Router) {
		r.Use(WithTimeout(defaultTimeout), WithBodyCap(defaultBodyCap))
		mount(r)
	})
	return r
}

// mountAPI registers the generated routes, each behind the authentication its
// operation in spec declares and validated against spec, and answers their
// decode, parameter and handler errors as Problems.
func mountAPI(si gen.StrictServerInterface, spec *openapi3.T, serviceKey string, log *slog.Logger) func(chi.Router) {
	ops, err := indexOperations(spec)
	if err != nil {
		// TestEveryOperationDeclaresSecurity holds openapi.yaml to the rule
		// the index enforces, so this too is a broken build.
		panic(fmt.Sprintf("httpapi: index the OpenAPI operations: %v", err))
	}
	validate := validateRequests(spec)
	checkServiceKey := requireServiceKey(serviceKey, ops, log)
	return func(r chi.Router) {
		strict := gen.NewStrictHandlerWithOptions(si, nil, gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestError(log),
			ResponseErrorHandlerFunc: responseError(log),
		})
		gen.HandlerWithOptions(strict, gen.ChiServerOptions{
			BaseRouter: r,
			// The generated wrapper wraps each entry around the ones before
			// it, so the last entry runs first. Validation is first so that it
			// runs last: authentication, role check and rate limit go after it,
			// and a caller without the key learns nothing about the contract.
			Middlewares:      []gen.MiddlewareFunc{validate, checkServiceKey},
			ErrorHandlerFunc: paramError(log),
		})
	}
}
