package web

import (
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/ratelimit"
)

// Rule allows Limit requests per subject per Window. A rule that fails closed
// answers 503 while Redis is down; the others let requests through.
type Rule struct {
	Name       string
	Limit      int
	Window     time.Duration
	By         func(*gin.Context) string
	FailClosed bool
}

type Limiter struct {
	counter *ratelimit.Limiter
}

var (
	perServiceKey = Rule{
		Name:   "service-key",
		Limit:  1200,
		Window: time.Minute,
		By:     ByServiceKey,
	}
	perSession = Rule{
		Name:   "session",
		Limit:  300,
		Window: time.Minute,
		By:     bearerToken,
	}
)

var errNoCounter = errors.New("no rate limit counter")

func ByVisitorIP(c *gin.Context) string {
	return ClientIP(c.Request.Context()).String()
}

func ByToken(c *gin.Context) string {
	return c.Param("token")
}

func ByServiceKey(c *gin.Context) string {
	return c.GetHeader("X-Service-Key")
}

func ByUser(c *gin.Context) string {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID.String()
}

// NewLimiter counts in Redis through counter; nil acts as if Redis were down.
func NewLimiter(counter *ratelimit.Limiter) *Limiter {
	return &Limiter{counter: counter}
}

func (limiter *Limiter) Limit(rule Rule) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := c.Request.Context()
		allowed := false
		var wait time.Duration
		err := errNoCounter
		if limiter.counter != nil {
			allowed, wait, err = limiter.counter.Allow(ctx, rule.Name, rule.By(c), rule.Limit, rule.Window)
		}

		if err != nil {
			slog.ErrorContext(ctx, "rate_limit_unavailable",
				"request_id", RequestID(ctx),
				"group", rule.Name,
				"err", err)
			if rule.FailClosed {
				abort(c, apperr.New(apperr.Unavailable, "This is unavailable; try again shortly."))
				return
			}
			c.Next()
			return
		}
		if !allowed {
			abort(c, apperr.RateLimit(wait))
			return
		}
		c.Next()
	}
}
