package linkedin

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type connectionResponse struct {
	Status      string    `json:"status"`
	MemberName  string    `json:"memberName,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitzero"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
}

type authorizeResponse struct {
	AuthorizeURL string `json:"authorizeUrl"`
}

type callbackInput struct {
	Code  string `json:"code" binding:"required,max=2000"`
	State string `json:"state" binding:"required,max=500"`
}

func (h *Handler) getConnection(c *gin.Context) {
	conn, err := h.Connector.Status(c.Request.Context(), h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, connectionView(conn))
}

func (h *Handler) startConnection(c *gin.Context) {
	link, err := h.Connector.AuthorizeURL(userID(c), h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, authorizeResponse{AuthorizeURL: link})
}

func (h *Handler) finishConnection(c *gin.Context) {
	var in callbackInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	conn, err := h.Connector.Finish(ctx, userID(c), in.Code, in.State, h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "linkedin_connected", "request_id", web.RequestID(ctx), "status", conn.Status)
	c.JSON(http.StatusOK, connectionView(conn))
}

func (h *Handler) disconnect(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.Connector.Disconnect(ctx); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "linkedin_disconnected", "request_id", web.RequestID(ctx))
	c.Status(http.StatusNoContent)
}

func userID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}

func connectionView(c Connection) connectionResponse {
	v := connectionResponse{Status: c.Status, MemberName: c.MemberName, ConnectedAt: c.ConnectedAt.UTC()}
	if c.ExpiresAt.Valid {
		v.ExpiresAt = c.ExpiresAt.Time.UTC()
	}
	return v
}
