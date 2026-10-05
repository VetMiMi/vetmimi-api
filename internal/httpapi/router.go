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
	return router(log, mountAPI(&server{deps}, spec, log))
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

// mountAPI registers the generated routes, each validated against spec, and
// answers their decode, parameter and handler errors as Problems.
func mountAPI(si gen.StrictServerInterface, spec *openapi3.T, log *slog.Logger) func(chi.Router) {
	validate := validateRequests(spec)
	return func(r chi.Router) {
		strict := gen.NewStrictHandlerWithOptions(si, nil, gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestError,
			ResponseErrorHandlerFunc: responseError(log),
		})
		gen.HandlerWithOptions(strict, gen.ChiServerOptions{
			BaseRouter: r,
			// The generated wrapper wraps each entry around the ones before
			// it, so the last entry runs first. Validation is first so that it
			// runs last: authentication, role check and rate limit go after it.
			Middlewares:      []gen.MiddlewareFunc{validate},
			ErrorHandlerFunc: paramError,
		})
	}
}
