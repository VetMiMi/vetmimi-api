package video

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

var joinToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type tokenPath struct {
	Token string `uri:"token" binding:"required"`
}

type sessionResponse struct {
	State    string          `json:"state"`
	OpensAt  time.Time       `json:"opensAt"`
	ClosesAt time.Time       `json:"closesAt"`
	StartsAt time.Time       `json:"startsAt"`
	EndsAt   time.Time       `json:"endsAt"`
	Timezone string          `json:"timezone"`
	Service  serviceResponse `json:"service"`
	Locale   string          `json:"locale"`
}

type serviceResponse struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// getSession answers an unknown token with the same 404 as any other link.
func (h *Handler) getSession(c *gin.Context) {
	var path tokenPath
	if !web.BindURI(c, &path) {
		return
	}
	if !checkJoinToken(c, path.Token) {
		return
	}
	ctx := c.Request.Context()

	session, err := FindSession(ctx, db.New(h.Pool), path.Token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newSessionResponse(session))
}

func (h *Handler) createClientTicket(c *gin.Context) {
	var path tokenPath
	if !web.BindURI(c, &path) {
		return
	}
	if !checkJoinToken(c, path.Token) {
		return
	}
	ctx := c.Request.Context()

	ticket, err := JoinAsClient(ctx, db.New(h.Pool), h.Issuer, path.Token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logTicket(ctx, ticket)
	c.JSON(http.StatusCreated, ticket)
}

func newSessionResponse(session Session) sessionResponse {
	return sessionResponse{
		State:    session.State,
		OpensAt:  session.OpensAt,
		ClosesAt: session.ClosesAt,
		StartsAt: session.StartsAt,
		EndsAt:   session.EndsAt,
		Timezone: session.Timezone,
		Service: serviceResponse{
			Slug: session.ServiceSlug,
			Name: session.ServiceName,
		},
		Locale: session.Locale,
	}
}

// checkJoinToken refuses a token that cannot be a join token. When it returns false it has answered 400.
func checkJoinToken(c *gin.Context, token string) bool {
	if !joinToken.MatchString(token) {
		web.Error(c, apperr.Invalid("The request is not valid.",
			apperr.FieldError{Field: "token", Message: "must match the pattern " + joinToken.String()}))
		return false
	}
	return true
}

// logTicket logs the room only: the token and the ticket are as good as passwords.
func logTicket(ctx context.Context, ticket Ticket) {
	slog.InfoContext(ctx, "room_ticket_issued",
		"request_id", web.RequestID(ctx),
		"room_id", ticket.RoomID.String(),
		"role", ticket.Role,
	)
}
