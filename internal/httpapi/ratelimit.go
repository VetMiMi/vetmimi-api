package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
)

// keyBy is what a limit counts requests by.
type keyBy int

const (
	byVisitorIP keyBy = iota
	byPathToken
	byServiceKey
	bySessionToken
	byRoomID
	byUser
)

// rateLimit allows limit requests per subject per window. A group that fails
// closed answers 503 while Redis is unreachable.
type rateLimit struct {
	group      string
	key        keyBy
	limit      int
	window     time.Duration
	failClosed bool
}

// The rate limits. They protect the API, so they are constants, not settings.
// The per-email sign-in limit is in the auth package.
var (
	// Sign-in fails closed, where a missing limit helps an attacker most; the
	// rest stay open, so losing Redis never stops bookings.
	signIn       = rateLimit{"sign-in", byVisitorIP, 5, time.Minute, true}
	manageLink   = rateLimit{"manage", byPathToken, 20, time.Hour, false}
	videoSession = rateLimit{"video-session", byPathToken, 30, time.Minute, false}

	limitsByOperation = map[string]rateLimit{
		"createSession":            signIn,
		"createPublicAppointment":  {"booking", byVisitorIP, 5, 10 * time.Minute, false},
		"createContactEnquiry":     {"contact", byVisitorIP, 5, time.Hour, false},
		"getPublicAvailability":    {"availability", byVisitorIP, 60, time.Minute, false},
		"getManagedAppointment":    manageLink,
		"cancelManagedAppointment": manageLink,
		"requestManagedReschedule": manageLink,
		"getPublicSession":         videoSession,
		"createRoomTicket":         videoSession,
		"suggestPostVersions":      {"ai-suggestions", byUser, 20, time.Hour, false},
	}
	otherPublicCalls = rateLimit{"service-key", byServiceKey, 1200, time.Minute, false}
	signedInCalls    = rateLimit{"session", bySessionToken, 300, time.Minute, false}
	roomUpgrade      = rateLimit{"room", byRoomID, 20, time.Minute, false}
)

// limitFor is op's limit. Operations with security: [] have none, so the
// probes answer while Redis is down; the WebSocket counts its own.
func limitFor(op operation) (rateLimit, bool) {
	if l, ok := limitsByOperation[op.ID]; ok {
		return l, true
	}
	switch op.Security {
	case schemeServiceKey:
		return otherPublicCalls, true
	case schemeSessionToken:
		return signedInCalls, true
	}
	return rateLimit{}, false
}

// errNoLimiter makes a router built without a Limiter act as if Redis were
// down, not as if there were no limits.
var errNoLimiter = errors.New("no rate limiter")

type RateLimits struct {
	limiter *ratelimit.Limiter
	log     *slog.Logger
	now     clock.Now

	mu         sync.Mutex
	quietUntil time.Time // a Redis outage is logged once a minute, not once a request
}

// NewRateLimits counts with limiter; nil acts as an unreachable Redis.
func NewRateLimits(limiter *ratelimit.Limiter, log *slog.Logger, now clock.Now) *RateLimits {
	return &RateLimits{limiter: limiter, log: log, now: now}
}

// operations counts each request against its operation's limit.
func (rl *RateLimits) operations(ops operations) gen.MiddlewareFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			op, _ := ops.lookup(r)
			l, ok := limitFor(op)
			if !ok || rl.admit(w, r, l, subject(r, l.key)) {
				next.ServeHTTP(w, r)
			}
		})
	}
}

// AllowRoomUpgrade counts a WebSocket upgrade to roomID. When it returns
// false it has answered the request.
func (rl *RateLimits) AllowRoomUpgrade(w http.ResponseWriter, r *http.Request, roomID string) bool {
	return rl.admit(w, r, roomUpgrade, roomID)
}

// subject is what r is counted by. The visitor IP can be trusted because the
// service key was checked first.
func subject(r *http.Request, key keyBy) string {
	switch key {
	case byVisitorIP:
		return ClientIP(r.Context()).String()
	case byPathToken:
		return chi.URLParam(r, "token")
	case byServiceKey:
		return r.Header.Get("X-Service-Key")
	case bySessionToken:
		return bearerToken(r)
	case byUser:
		session, _ := auth.FromContext(r.Context())
		return session.User.ID.String()
	}
	return ""
}

// admit counts r and reports whether it may go on; if not, it has answered.
func (rl *RateLimits) admit(w http.ResponseWriter, r *http.Request, l rateLimit, subject string) bool {
	allowed, retryAfter, err := rl.allow(r.Context(), l, subject)
	switch {
	case err != nil:
		rl.unavailable(r, l, err)
		if l.failClosed {
			writeProblem(w, apperr.New(apperr.Unavailable, "Sign-in is unavailable; try again shortly."))
			return false
		}
		return true
	case !allowed:
		writeProblem(w, apperr.RateLimit(retryAfter))
		return false
	}
	return true
}

func (rl *RateLimits) allow(ctx context.Context, l rateLimit, subject string) (bool, time.Duration, error) {
	if rl.limiter == nil {
		return false, 0, errNoLimiter
	}
	return rl.limiter.Allow(ctx, l.group, subject, l.limit, l.window)
}

func (rl *RateLimits) unavailable(r *http.Request, l rateLimit, err error) {
	rl.mu.Lock()
	now := rl.now()
	quiet := now.Before(rl.quietUntil)
	if !quiet {
		rl.quietUntil = now.Add(time.Minute)
	}
	rl.mu.Unlock()
	if !quiet {
		rl.log.ErrorContext(r.Context(), "rate_limit_unavailable",
			"request_id", RequestID(r.Context()), "group", l.group, "err", err)
	}
}
