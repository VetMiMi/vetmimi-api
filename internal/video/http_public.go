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

type sessionResponse struct {
	State    string     `json:"state"`
	OpensAt  time.Time  `json:"opensAt"`
	ClosesAt time.Time  `json:"closesAt"`
	StartsAt time.Time  `json:"startsAt"`
	EndsAt   time.Time  `json:"endsAt"`
	Timezone string     `json:"timezone"`
	Service  serviceRef `json:"service"`
	Locale   string     `json:"locale"`
}

type serviceRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// getSession answers an unknown token with the same 404 as any other link.
func (h *Handler) getSession(c *gin.Context) {
	token, ok := tokenParam(c)
	if !ok {
		return
	}
	s, err := FindSession(c.Request.Context(), db.New(h.Pool), token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, sessionResponse{
		State:    s.State,
		OpensAt:  s.OpensAt,
		ClosesAt: s.ClosesAt,
		StartsAt: s.StartsAt,
		EndsAt:   s.EndsAt,
		Timezone: s.Timezone,
		Service:  serviceRef{Slug: s.ServiceSlug, Name: s.ServiceName},
		Locale:   s.Locale,
	})
}

func (h *Handler) createClientTicket(c *gin.Context) {
	token, ok := tokenParam(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	t, err := JoinAsClient(ctx, db.New(h.Pool), h.Issuer, token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	logTicket(ctx, t)
	c.JSON(http.StatusCreated, t)
}

func tokenParam(c *gin.Context) (string, bool) {
	token := c.Param("token")
	if !joinToken.MatchString(token) {
		web.Error(c, apperr.Invalid("The request is not valid.",
			apperr.FieldError{Field: "token", Message: "must match the pattern " + joinToken.String()}))
		return "", false
	}
	return token, true
}

// logTicket logs the room only: the token and the ticket are as good as passwords.
func logTicket(ctx context.Context, t Ticket) {
	slog.InfoContext(ctx, "room_ticket_issued", "request_id", web.RequestID(ctx),
		"room_id", t.RoomID.String(), "role", t.Role)
}
