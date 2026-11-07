package signin

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type signInBody struct {
	Email    string  `json:"email" binding:"required,email,max=254"`
	Password string  `json:"password" binding:"required,min=12,max=200"`
	TotpCode *string `json:"totpCode" binding:"omitnil,totp"`
}

type sessionResponse struct {
	Token     string       `json:"token"`
	ExpiresAt time.Time    `json:"expiresAt"`
	User      userResponse `json:"user"`
}

type userResponse struct {
	ID           pgtype.UUID `json:"id"`
	Email        string      `json:"email"`
	DisplayName  string      `json:"displayName"`
	Roles        []string    `json:"roles"`
	Practitioner bool        `json:"isPractitioner"`
	TwoStep      bool        `json:"twoStepEnabled"`
}

// signIn logs a locked-out email by a hash prefix only.
func (h *Handler) signIn(c *gin.Context) {
	var body signInBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	credentials := auth.Credentials{
		Email:    body.Email,
		Password: body.Password,
	}
	if body.TotpCode != nil {
		credentials.Code = *body.TotpCode
	}
	signedIn, err := h.Sessions.SignIn(ctx, credentials)
	if err != nil {
		var refused *apperr.Error
		if errors.As(err, &refused) && refused.Code == apperr.RateLimited {
			slog.WarnContext(ctx, "sign_in_locked", "request_id", web.RequestID(ctx), "email_hash", auth.EmailHashPrefix(body.Email))
		}
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "signed_in", "request_id", web.RequestID(ctx), "user_id", signedIn.Session.User.ID.String())
	c.JSON(http.StatusCreated, newSessionResponse(signedIn))
}

func (h *Handler) signOut(c *gin.Context) {
	ctx := c.Request.Context()

	session, _ := auth.FromContext(ctx)
	err := h.Sessions.SignOut(ctx, session.ID)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "signed_out", "request_id", web.RequestID(ctx), "user_id", session.User.ID.String())
	c.Status(http.StatusNoContent)
}

func (h *Handler) currentUser(c *gin.Context) {
	ctx := c.Request.Context()

	session, _ := auth.FromContext(ctx)

	c.JSON(http.StatusOK, newUserResponse(session.User))
}

func newSessionResponse(signedIn auth.SignedIn) sessionResponse {
	return sessionResponse{
		Token:     signedIn.Token,
		ExpiresAt: signedIn.ExpiresAt.UTC(),
		User:      newUserResponse(signedIn.Session.User),
	}
}

func newUserResponse(user auth.User) userResponse {
	return userResponse{
		ID:           user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		Roles:        user.Roles,
		Practitioner: user.Practitioner,
		TwoStep:      user.TwoStep,
	}
}
