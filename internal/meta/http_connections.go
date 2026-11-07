package meta

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

type choosePageBody struct {
	PageID string `json:"pageId" binding:"required,max=100"`
}

type connectionResponse struct {
	Status            string         `json:"status"`
	PageID            string         `json:"pageId,omitempty"`
	PageName          string         `json:"pageName,omitempty"`
	InstagramID       string         `json:"instagramId,omitempty"`
	InstagramUsername string         `json:"instagramUsername,omitempty"`
	ExpiresAt         time.Time      `json:"expiresAt,omitzero"`
	LastError         string         `json:"lastError,omitempty"`
	ConnectedAt       time.Time      `json:"connectedAt,omitzero"`
	Pages             []pageResponse `json:"pages,omitzero"` // omitted when nil, [] when empty
}

// pageResponse leaves out the Page's access token.
type pageResponse struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	InstagramUsername string `json:"instagramUsername,omitempty"`
}

type authorizeResponse struct {
	AuthorizeURL string `json:"authorizeUrl"`
}

func (h *Handler) getConnection(c *gin.Context) {
	ctx := c.Request.Context()

	connection, err := h.Connector.Status(ctx)
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

	slog.InfoContext(ctx, "meta_disconnected", "request_id", web.RequestID(ctx))
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

	slog.InfoContext(ctx, "meta_signed_in", "request_id", web.RequestID(ctx), "status", connection.Status)
	c.JSON(http.StatusOK, newConnectionResponse(connection))
}

func (h *Handler) choosePage(c *gin.Context) {
	var body choosePageBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	connection, err := h.Connector.ChoosePage(ctx, currentUserID(c), body.PageID, h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "meta_connected", "request_id", web.RequestID(ctx), "page_id", connection.PageID)
	c.JSON(http.StatusOK, newConnectionResponse(connection))
}

// newConnectionResponse lists pages only while one is being chosen.
func newConnectionResponse(connection Connection) connectionResponse {
	response := connectionResponse{
		Status:            connection.Status,
		PageID:            connection.PageID,
		PageName:          connection.PageName,
		InstagramID:       connection.InstagramID,
		InstagramUsername: connection.InstagramUsername,
		LastError:         connection.LastError,
		ConnectedAt:       connection.ConnectedAt.UTC(),
	}
	if connection.ExpiresAt.Valid {
		response.ExpiresAt = connection.ExpiresAt.Time.UTC()
	}
	if connection.Status != "choosing_page" {
		return response
	}
	response.Pages = make([]pageResponse, len(connection.Pages))
	for i, page := range connection.Pages {
		response.Pages[i] = pageResponse{ID: page.ID, Name: page.Name}
		if page.Instagram != nil {
			response.Pages[i].InstagramUsername = page.Instagram.Username
		}
	}
	return response
}

func currentUserID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}
