// Package httpapi serves the API in openapi.yaml. router.go builds the chain:
// shared middleware, then per operation the service key, session, role, rate
// limit and validation checks; each handler parses, calls a domain function and maps the result.
package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
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

// Deps are what the handlers need. cmd/api fills them; tests leave most nil.
type Deps struct {
	PingPostgres   func(context.Context) error // nil: readiness reports it failing
	PingRedis      func(context.Context) error // nil: readiness reports it failing
	Log            *slog.Logger                // nil: slog.Default()
	ServiceKey     string                      // empty: every public call is refused
	RateLimits     *RateLimits                 // nil: counts as Redis being down
	Sessions       *auth.Sessions              // nil: every signed-in call is refused
	Pool           *pgxpool.Pool
	Queue          *queue.Queue // nil: tasks are dropped and the sweepers rebuild them
	SigningSecret  []byte       // signs management links, join links and room tickets
	PublicAPIURL   string
	TURNHost       string // empty: room tickets offer STUN only
	TURNSecret     string
	SiteURL        string     // the only origin the video WebSocket accepts
	Hub            *video.Hub // nil: a new one
	Media          *media.Store
	MediaPublicURL string              // where the bucket's public/ prefix is served
	Meta           *meta.Connector     // nil: its routes answer unavailable
	LinkedIn       *linkedin.Connector // nil: its routes answer unavailable
	Assistant      *assistant.Client   // nil or without a key: off
	Now            clock.Now           // nil: time.Now
}

// server implements gen.StrictServerInterface; its methods are in the area files.
type server struct {
	Deps
}

// enqueue queues tasks once the change that led to them has committed. A
// failure is logged by the queue and left to the sweepers.
func (s *server) enqueue(ctx context.Context, tasks ...queue.Task) {
	if s.Queue != nil {
		s.Queue.Enqueue(ctx, tasks...)
	}
}

// NewRouter returns the whole HTTP API. Every error, unknown routes included,
// is an application/problem+json body.
func NewRouter(deps Deps) http.Handler {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.RateLimits == nil {
		deps.RateLimits = NewRateLimits(nil, deps.Log, time.Now)
	}
	if deps.Hub == nil {
		deps.Hub = video.NewHub(deps.Pool, deps.Log, deps.Now)
	}
	spec, err := gen.GetSpec()
	if err != nil {
		// The spec is embedded at build time, so this is a broken build.
		panic(fmt.Sprintf("httpapi: decode the embedded OpenAPI spec: %v", err))
	}
	s := &server{deps}
	return router(deps.Log, s.connectVideoRoom, mountAPI(s, spec, deps))
}

// router wraps the shared middleware around the video WebSocket ws and the
// routes mount registers. Tests use it to put stub routes behind the real chain.
func router(log *slog.Logger, ws http.HandlerFunc, mount func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Use(requestID, realIP, logRequests(log), recoverPanics(log), setSecurityHeaders)
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)

	// The WebSocket stays open for a whole session, so it skips the timeout and body cap.
	if ws != nil {
		r.Get(roomSocketPattern, ws)
	}
	r.Group(func(r chi.Router) {
		r.Use(WithTimeout(defaultTimeout), WithBodyCap(defaultBodyCap))
		mount(r)
	})
	return r
}

// mountAPI registers the generated routes behind the per-operation checks.
func mountAPI(si gen.StrictServerInterface, spec *openapi3.T, deps Deps) func(chi.Router) {
	log := deps.Log
	ops, err := indexOperations(spec)
	if err != nil {
		// TestEveryOperationDeclaresSecurity keeps openapi.yaml valid, so this is a broken build.
		panic(fmt.Sprintf("httpapi: index the OpenAPI operations: %v", err))
	}
	upload, mountUpload := si.(*server)
	if mountUpload {
		// Generation leaves uploadMedia out of the embedded spec.
		ops[operationKey(http.MethodPost, uploadMediaPattern)] = uploadMediaOperation
	}

	checkServiceKey := requireServiceKey(deps.ServiceKey, ops, log)
	checkSession := requireSession(deps.Sessions, ops, log)
	checkRoles := requireRoles(ops)
	rateLimit := deps.RateLimits.operations(ops)
	bodyCaps := operationLimits(ops)
	validate := validateRequests(spec)

	return func(r chi.Router) {
		strict := gen.NewStrictHandlerWithOptions(si, nil, gen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  requestError(log),
			ResponseErrorHandlerFunc: responseError(log),
		})
		gen.HandlerWithOptions(strict, gen.ChiServerOptions{
			BaseRouter: r,
			// The generated code runs the last entry first: key, session, role,
			// rate limit, body cap, validation. A caller without a key, session or
			// role is never counted; a malformed request still is.
			Middlewares:      []gen.MiddlewareFunc{validate, bodyCaps, rateLimit, checkRoles, checkSession, checkServiceKey},
			ErrorHandlerFunc: paramError(log),
		})
		// Upload reads its own multipart body, so it skips validation but keeps
		// the other checks in the same order, with a larger cap and deadline.
		if mountUpload {
			r.With(WithTimeout(uploadTimeout), WithBodyCap(uploadBodyCap),
				checkServiceKey, checkSession, checkRoles, rateLimit).
				Post(uploadMediaPattern, upload.uploadMedia)
		}
	}
}
