package httpapi

import (
	"context"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func (s *server) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.HealthStatusOk}, nil
}

// GetReadyz answers 503 while PostgreSQL or Redis is unreachable, so the host
// keeps traffic away from a half-started process.
func (s *server) GetReadyz(ctx context.Context, _ gen.GetReadyzRequestObject) (gen.GetReadyzResponseObject, error) {
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
