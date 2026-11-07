// Package signin is the HTTP side of internal/auth: sign in, sign out and the
// current user. It is its own package because internal/web imports internal/auth.
package signin

import (
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type Handler struct {
	Sessions *auth.Sessions
}

var signInLimit = web.Rule{
	Name:       "sign-in",
	Limit:      5,
	Window:     time.Minute,
	By:         web.ByVisitorIP,
	FailClosed: true,
}

func (h *Handler) Routes(r web.Routes) {
	r.Public.POST("/auth/sessions", r.Limit(signInLimit), h.signIn)

	anyone := r.Admin(auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin)
	anyone.DELETE("/auth/sessions/current", h.signOut)
	anyone.GET("/auth/me", h.currentUser)
}
