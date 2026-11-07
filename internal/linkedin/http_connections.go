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

type finishConnectionBody struct {
	Code  string `json:"code" binding:"required,max=2000"`
	State string `json:"state" binding:"required,max=500"`
}

type connectionResponse struct {
	Status      string    `json:"status"`
	MemberName  string    `json:"memberName,omitempty"`
	ExpiresAt   time.Time `json:"expiresAt,omitzero"`
	ConnectedAt time.Time `json:"connectedAt,omitzero"`
}

type authorizeResponse struct {
	AuthorizeURL string `json:"authorizeUrl"`
}

func (h *Handler) getConnection(c *gin.Context) {
	ctx := c.Request.Context()

	connection, err := h.Connector.Status(ctx, h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newConnectionResponse(connection))
}

func (h *Handler) disconnect(c *gin.Context) {
	ctx := c.Request.Context()

	err := h.Connector.Disconnect(ctx)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "linkedin_disconnected", "request_id", web.RequestID(ctx))
	c.Status(http.StatusNoContent)
}

func (h *Handler) startConnection(c *gin.Context) {
	link, err := h.Connector.AuthorizeURL(currentUserID(c), h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, authorizeResponse{AuthorizeURL: link})
}

func (h *Handler) finishConnection(c *gin.Context) {
	var body finishConnectionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	connection, err := h.Connector.Finish(ctx, currentUserID(c), body.Code, body.State, h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "linkedin_connected", "request_id", web.RequestID(ctx), "status", connection.Status)
	c.JSON(http.StatusOK, newConnectionResponse(connection))
}

func newConnectionResponse(connection Connection) connectionResponse {
	response := connectionResponse{
		Status:      connection.Status,
		MemberName:  connection.MemberName,
		ConnectedAt: connection.ConnectedAt.UTC(),
	}
	if connection.ExpiresAt.Valid {
		response.ExpiresAt = connection.ExpiresAt.Time.UTC()
	}
	return response
}

func currentUserID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}
