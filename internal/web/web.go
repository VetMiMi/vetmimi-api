// Package web is the HTTP layer every feature shares: the router, the route
// groups, sign-in and role checks, rate limits, binding and error responses.
package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

// Routes are the groups each feature registers its routes on.
type Routes struct {
	Public  *gin.RouterGroup // called by the website's server with the service key
	Root    *gin.Engine      // no deadline, body cap or sign-in: the video WebSocket checks its own
	admin   *gin.RouterGroup
	slow    *gin.RouterGroup
	limiter *Limiter
}

// Admin is for a signed-in user holding one of roles. Taking the roles here means no admin route can forget them.
func (r Routes) Admin(roles ...auth.Role) *gin.RouterGroup {
	return r.admin.Group("", Role(roles...))
}

// Slow is Admin with a long deadline and a large body: uploads and the AI assistant.
func (r Routes) Slow(roles ...auth.Role) *gin.RouterGroup {
	return r.slow.Group("", Role(roles...))
}

// NewRouter returns a router with the middleware every request gets.
func NewRouter() *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.Use(requestID, logRequests, recoverPanics, securityHeaders)
	r.NoRoute(func(c *gin.Context) {
		abort(c, apperr.New(apperr.NotFound, "No route matches this method and path."))
	})
	return r
}

func NewRoutes(r *gin.Engine, serviceKey string, sessions *auth.Sessions, limiter *Limiter) Routes {
	signedIn := SignedIn(sessions)
	return Routes{
		Public: r.Group("", Timeout(30*time.Second), MaxBody(64<<10),
			ServiceKey(serviceKey), limiter.Limit(perServiceKey)),
		admin: r.Group("", Timeout(30*time.Second), MaxBody(512<<10),
			signedIn, limiter.Limit(perSession)),
		slow: r.Group("", Timeout(2*time.Minute), MaxBody(21<<20),
			signedIn, limiter.Limit(perSession)),
		Root:    r,
		limiter: limiter,
	}
}

// Limit counts each request to a route against rule, on top of its group's limit.
func (r Routes) Limit(rule Rule) gin.HandlerFunc {
	return r.limiter.Limit(rule)
}

type contextKey int

const (
	requestIDKey contextKey = iota
	clientIPKey
)

func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// ClientIP is the visitor's address, or the zero Addr outside a request.
func ClientIP(ctx context.Context) netip.Addr {
	ip, _ := ctx.Value(clientIPKey).(netip.Addr)
	return ip
}

func setContext(c *gin.Context, key contextKey, value any) {
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), key, value))
}

var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// requestID reuses the site's X-Request-Id, so both logs share one id, when it is safe to log.
func requestID(c *gin.Context) {
	id := c.GetHeader("X-Request-Id")
	if !safeRequestID.MatchString(id) {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
	}
	c.Header("X-Request-Id", id)
	setContext(c, requestIDKey, id)
	setContext(c, clientIPKey, clientAddr(c.Request))
	c.Next()
}

// clientAddr trusts only the last X-Forwarded-For hop: Caddy appends the one it saw.
func clientAddr(r *http.Request) netip.Addr {
	if lines := r.Header.Values("X-Forwarded-For"); len(lines) > 0 {
		hops := strings.Split(lines[len(lines)-1], ",")
		if ip, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
			return ip.Unmap()
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap()
	}
	return netip.Addr{}
}

// logRequests logs the route pattern, never the path: links carry tokens in their paths.
func logRequests(c *gin.Context) {
	start := time.Now()
	c.Next()
	slog.InfoContext(c.Request.Context(), "request",
		"request_id", RequestID(c.Request.Context()),
		"method", c.Request.Method,
		"route", c.FullPath(),
		"status", c.Writer.Status(),
		"duration_ms", time.Since(start).Milliseconds(),
		"bytes", max(c.Writer.Size(), 0))
}

func recoverPanics(c *gin.Context) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if p == http.ErrAbortHandler {
			panic(p)
		}
		slog.ErrorContext(c.Request.Context(), "panic",
			"request_id", RequestID(c.Request.Context()), "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
		if !c.Writer.Written() {
			abort(c, apperr.New(apperr.InternalError, "Unexpected failure."))
		}
	}()
	c.Next()
}

// securityHeaders: the API only answers JSON, so nothing it sends should be framed, sniffed or cached.
func securityHeaders(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-store")
	c.Next()
}

// Timeout gives the handler a deadline; a database call past it fails and is answered 503.
func Timeout(d time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), d)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// MaxBody makes reading more than n bytes of the body fail, answered 413.
func MaxBody(n int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, n)
		c.Next()
	}
}
