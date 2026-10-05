package httpapi

import (
	"log/slog"
	"net/http"

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
	return router(log, mountAPI(&server{deps}, log))
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

// mountAPI registers the generated routes, answering their decode, parameter
// and handler errors as Problems.
func mountAPI(si gen.StrictServerInterface, log *slog.Logger) func(chi.Router) {
	return func(r chi.Router) {
		strict := gen.NewStrictHandlerWithOptions(si, nil, gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestError,
			ResponseErrorHandlerFunc: responseError(log),
		})
		gen.HandlerWithOptions(strict, gen.ChiServerOptions{
			BaseRouter:       r,
			ErrorHandlerFunc: paramError,
		})
	}
}
