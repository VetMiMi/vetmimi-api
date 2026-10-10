// Package httpapi adapts the generated OpenAPI server interface to the domain
// packages. Handlers parse, call a domain function, and map the result.
package httpapi

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// Deps are what the handlers need, set by cmd/api once the database pool and
// Redis client exist. A nil ping makes readiness report that dependency as
// failing, which is the truth. Log receives requests and unexpected
// failures; nil means slog.Default(). ServiceKey is the key public operations
// and sign-in require; empty, they refuse every call. RateLimits counts
// requests; nil behaves as if Redis were unreachable. Sessions signs
// administrators in and authenticates them; nil, signed-in operations refuse
// every call. Pool is the database the domain handlers use. Queue takes the
// tasks a handler enqueues after its transaction commits; nil drops them, and
// the sweepers rebuild them (tests leave it nil). SigningSecret derives
// management and join links and room tickets. PublicAPIURL, TURNHost and
// TURNSecret go into room tickets; TURNHost empty leaves TURN out. SiteURL is
// the only origin the video WebSocket accepts. Hub holds the open video
// rooms; nil gets a fresh one. Media is the bucket images are uploaded to;
// nil refuses uploads. MediaPublicURL is where the bucket's public/ prefix
// is served. Meta is the Facebook Page connection and LinkedIn the profile
// connection; nil refuses their routes. Assistant suggests channel versions;
// nil or without a key, it is off. Now is the clock; nil means time.Now.
type Deps struct {
	PingPostgres   func(context.Context) error
	PingRedis      func(context.Context) error
	Log            *slog.Logger
	ServiceKey     string
	RateLimits     *RateLimits
	Sessions       *auth.Sessions
	Pool           *pgxpool.Pool
	Queue          *queue.Queue
	SigningSecret  []byte
	PublicAPIURL   string
	TURNHost       string
	TURNSecret     string
	SiteURL        string
	Hub            *video.Hub
	Media          *media.Store
	MediaPublicURL string
	Meta           *meta.Connector
	LinkedIn       *linkedin.Connector
	Assistant      *assistant.Client
	Now            clock.Now
}

// server implements gen.StrictServerInterface.
type server struct {
	Deps
}

// GetHealthz reports that the process is up.
func (s *server) GetHealthz(_ context.Context, _ gen.GetHealthzRequestObject) (gen.GetHealthzResponseObject, error) {
	return gen.GetHealthz200JSONResponse{Status: gen.HealthStatusOk}, nil
}

// GetReadyz pings PostgreSQL and Redis with a one-second timeout each and
// answers 503 if either fails, so the host's health check keeps traffic away
// from a half-started process.
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
