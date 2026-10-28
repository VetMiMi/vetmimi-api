package main

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// authHandler lives here, not in internal/auth, because internal/web imports internal/auth.
type authHandler struct {
	Sessions *auth.Sessions
}

var signInLimit = web.Rule{Name: "sign-in", Limit: 5, Window: time.Minute, By: web.ByVisitorIP, FailClosed: true}

func (h *authHandler) Routes(r web.Routes) {
	r.Public.POST("/auth/sessions", r.Limit(signInLimit), h.signIn)
	everyRole := web.Role(auth.ContentEditor, auth.BookingAdmin, auth.SiteAdmin)
	r.Admin.DELETE("/auth/sessions/current", everyRole, h.signOut)
	r.Admin.GET("/auth/me", everyRole, h.currentUser)
}

type signInInput struct {
	Email    string `json:"email" binding:"required,email,max=254"`
	Password string `json:"password" binding:"required,min=12,max=200"`
	TotpCode string `json:"totpCode" binding:"omitempty,totp"`
}

type sessionCreated struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	User      auth.User `json:"user"`
}

// signIn logs a locked-out email by a hash prefix only.
func (h *authHandler) signIn(c *gin.Context) {
	var in signInInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	signedIn, err := h.Sessions.SignIn(ctx, auth.Credentials{Email: in.Email, Password: in.Password, Code: in.TotpCode})
	var refused *apperr.Error
	if errors.As(err, &refused) && refused.Code == apperr.RateLimited {
		slog.WarnContext(ctx, "sign_in_locked", "request_id", web.RequestID(ctx), "email_hash", auth.EmailHashPrefix(in.Email))
	}
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "signed_in", "request_id", web.RequestID(ctx), "user_id", signedIn.Session.User.ID.String())
	c.JSON(http.StatusCreated, sessionCreated{
		Token:     signedIn.Token,
		ExpiresAt: signedIn.ExpiresAt.UTC(),
		User:      signedIn.Session.User,
	})
}

func (h *authHandler) signOut(c *gin.Context) {
	ctx := c.Request.Context()
	session, _ := auth.FromContext(ctx)
	if err := h.Sessions.SignOut(ctx, session.ID); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "signed_out", "request_id", web.RequestID(ctx), "user_id", session.User.ID.String())
	c.Status(http.StatusNoContent)
}

func (h *authHandler) currentUser(c *gin.Context) {
	session, _ := auth.FromContext(c.Request.Context())
	c.JSON(http.StatusOK, session.User)
}
