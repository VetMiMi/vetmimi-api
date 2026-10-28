package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

// ServiceKey lets through only the website's server. It forwards the visitor's
// address in X-Visitor-IP, which becomes the client IP. The key is never logged.
func ServiceKey(key string) gin.HandlerFunc {
	want := sha256.Sum256([]byte(key)) // comparing digests hides the key's length
	return func(c *gin.Context) {
		got := sha256.Sum256([]byte(c.GetHeader("X-Service-Key")))
		if key == "" || subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
			abort(c, apperr.New(apperr.Unauthenticated, "A valid service key is required."))
			return
		}
		ip, err := netip.ParseAddr(c.GetHeader("X-Visitor-IP"))
		if err != nil {
			slog.WarnContext(c.Request.Context(), "visitor_ip_missing",
				"request_id", RequestID(c.Request.Context()), "route", c.FullPath())
		} else {
			setContext(c, clientIPKey, ip.Unmap())
		}
		c.Next()
	}
}

// SignedIn needs a live session as the bearer token and puts it in the context for auth.FromContext.
func SignedIn(sessions *auth.Sessions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if sessions == nil {
			abort(c, apperr.New(apperr.Unauthenticated, "A valid session is required."))
			return
		}
		session, err := sessions.Authenticate(c.Request.Context(), bearerToken(c))
		if err != nil {
			Error(c, err)
			return
		}
		c.Request = c.Request.WithContext(auth.WithSession(c.Request.Context(), session))
		c.Next()
	}
}

// Role lets through a user holding any of roles; a site admin may act as every role.
func Role(roles ...auth.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		session, _ := auth.FromContext(c.Request.Context())
		for _, r := range roles {
			if auth.HasRole(session.User.Roles, r) {
				c.Next()
				return
			}
		}
		abort(c, apperr.New(apperr.Forbidden, "Your role does not allow this."))
	}
}

func bearerToken(c *gin.Context) string {
	scheme, token, ok := strings.Cut(c.GetHeader("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return token
}
