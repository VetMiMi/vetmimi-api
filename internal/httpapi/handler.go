// Package httpapi adapts the generated OpenAPI server interface to the domain
// packages. Handlers parse, call a domain function, and map the result.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// Server implements gen.StrictServerInterface. The ping functions are set by
// cmd/api once the database pool and Redis client exist; until then readiness
// reports both as failing, which is the truth. Log receives unexpected
// failures; nil means slog.Default().
type Server struct {
	PingPostgres func(context.Context) error
	PingRedis    func(context.Context) error
	Log          *slog.Logger
}

// Handler returns the HTTP handler for every route in openapi.yaml. Every
// error it answers, including unknown routes, is an application/problem+json
// body.
func Handler(s *Server) http.Handler {
	log := s.Log
	if log == nil {
		log = slog.Default()
	}
	return handler(s, log)
}

func handler(si gen.StrictServerInterface, log *slog.Logger) http.Handler {
	r := chi.NewRouter()
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)
	strict := gen.NewStrictHandlerWithOptions(si, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc:  requestError,
		ResponseErrorHandlerFunc: responseError(log),
	})
	return gen.HandlerWithOptions(strict, gen.ChiServerOptions{
		BaseRouter:       r,
		ErrorHandlerFunc: paramError,
	})
}

// GetHealthz reports that the process is up.
func (s *Server) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.HealthStatusOk}, nil
}

// GetReadyz pings PostgreSQL and Redis with a one-second timeout each and
// answers 503 if either fails, so the host's health check keeps traffic away
// from a half-started process.
func (s *Server) GetReadyz(ctx context.Context, _ gen.GetReadyzRequestObject) (gen.GetReadyzResponseObject, error) {
	var r gen.Readiness
	r.Status = gen.ReadinessStatus("ok")
	r.Checks.Postgres = gen.ReadinessChecksPostgresOk
	r.Checks.Redis = gen.ReadinessChecksRedisOk

	if ping(ctx, s.PingPostgres) != nil {
		r.Checks.Postgres = gen.ReadinessChecksPostgresFail
		r.Status = gen.ReadinessStatus("unavailable")
	}
	if ping(ctx, s.PingRedis) != nil {
		r.Checks.Redis = gen.ReadinessChecksRedisFail
		r.Status = gen.ReadinessStatus("unavailable")
	}

	if r.Status != "ok" {
		return gen.GetReadyz503JSONResponse(r), nil
	}
	return gen.GetReadyz200JSONResponse(r), nil
}

func ping(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return context.DeadlineExceeded
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return fn(ctx)
}
