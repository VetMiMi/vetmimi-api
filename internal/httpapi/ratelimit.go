package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// keyBy names what a limit counts requests by.
type keyBy int

const (
	byVisitorIP keyBy = iota
	byPathToken
	byServiceKey
	bySessionToken
	byRoomID
	byUser
)

// rateLimit is how many requests one subject may make in each window of a
// group. A group that fails closed answers 503 while Redis is unreachable.
type rateLimit struct {
	group      string
	key        keyBy
	limit      int
	window     time.Duration
	failClosed bool
}

// The limits in docs/architecture.md, "Security → Rate limits". They are
// constants rather than settings rows: they protect the API, and are not
// business rules Daw Mi changes. The per-email sign-in limit is the auth
// domain's, because the email is in the body.
var (
	// Sign-in fails closed, because sign-in is where a missing limit helps
	// an attacker most; everything else stays open, because losing Redis
	// must not stop bookings (ADR-006).
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
		// Each suggestion is a paid model call.
		"suggestPostVersions": {"ai-suggestions", byUser, 20, time.Hour, false},
	}
	otherPublicCalls = rateLimit{"service-key", byServiceKey, 1200, time.Minute, false}
	signedInCalls    = rateLimit{"session", bySessionToken, 300, time.Minute, false}
	roomUpgrade      = rateLimit{"room", byRoomID, 20, time.Minute, false}
)

// limitFor is the limit op is counted against. Operations declaring
// security: [] have none: the probes must answer while Redis is down, and the
// WebSocket checks its own (AllowRoomUpgrade).
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

// errNoLimiter stands for Redis when the router was built without a Limiter,
// so an unwired router behaves as if Redis were down rather than unlimited.
var errNoLimiter = errors.New("no rate limiter")

// RateLimits applies the limits above with one platform.Limiter.
type RateLimits struct {
	limiter *platform.Limiter
	log     *slog.Logger
	now     clock.Now

	mu sync.Mutex
	// quietUntil is when rate_limit_unavailable may next be logged: a Redis
	// outage leaves a line a minute, not a line a request.
	quietUntil time.Time
}

// NewRateLimits returns the limits counted by limiter. A nil limiter counts
// nothing and is treated as an unreachable Redis.
func NewRateLimits(limiter *platform.Limiter, log *slog.Logger, now clock.Now) *RateLimits {
	return &RateLimits{limiter: limiter, log: log, now: now}
}

// operations returns the middleware that counts each request against its
// operation's limit. It runs after authentication, so a caller without a
// valid key or live session is refused without being counted, and before
// validation, so a flood of malformed requests is limited too.
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

// AllowRoomUpgrade counts a WebSocket upgrade to roomID, 20 a minute per
// room. The video handler calls it itself, because its route is mounted
// outside the chain that applies the other limits. When it returns false it
// has answered the request, and the handler must not upgrade.
func (rl *RateLimits) AllowRoomUpgrade(w http.ResponseWriter, r *http.Request, roomID string) bool {
	return rl.admit(w, r, roomUpgrade, roomID)
}

// subject is what r is counted by. The visitor IP is trustworthy here
// because the service key was checked first.
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

// admit counts r and reports whether it may go on; when it may not, it has
// answered 429, or 503 for a group that fails closed while Redis is down.
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

// unavailable logs rate_limit_unavailable at most once a minute.
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
