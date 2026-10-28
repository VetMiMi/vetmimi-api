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

type callbackInput struct {
	Code  string `json:"code" binding:"required,max=2000"`
	State string `json:"state" binding:"required,max=500"`
}

type pageChoiceInput struct {
	PageID string `json:"pageId" binding:"required,max=100"`
}

func (h *Handler) getConnection(c *gin.Context) {
	conn, err := h.Connector.Status(c.Request.Context())
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
	slog.InfoContext(ctx, "meta_signed_in", "request_id", web.RequestID(ctx), "status", conn.Status)
	c.JSON(http.StatusOK, connectionView(conn))
}

func (h *Handler) choosePage(c *gin.Context) {
	var in pageChoiceInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	conn, err := h.Connector.ChoosePage(ctx, userID(c), in.PageID, h.Connector.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "meta_connected", "request_id", web.RequestID(ctx), "page_id", conn.PageID)
	c.JSON(http.StatusOK, connectionView(conn))
}

func (h *Handler) disconnect(c *gin.Context) {
	ctx := c.Request.Context()
	if err := h.Connector.Disconnect(ctx); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "meta_disconnected", "request_id", web.RequestID(ctx))
	c.Status(http.StatusNoContent)
}

func userID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}

// connectionView lists pages only while one is being chosen.
func connectionView(c Connection) connectionResponse {
	v := connectionResponse{
		Status:            c.Status,
		PageID:            c.PageID,
		PageName:          c.PageName,
		InstagramID:       c.InstagramID,
		InstagramUsername: c.InstagramUsername,
		LastError:         c.LastError,
		ConnectedAt:       c.ConnectedAt.UTC(),
	}
	if c.ExpiresAt.Valid {
		v.ExpiresAt = c.ExpiresAt.Time.UTC()
	}
	if c.Status != "choosing_page" {
		return v
	}
	v.Pages = make([]pageResponse, len(c.Pages))
	for i, p := range c.Pages {
		v.Pages[i] = pageResponse{ID: p.ID, Name: p.Name}
		if p.Instagram != nil {
			v.Pages[i].InstagramUsername = p.Instagram.Username
		}
	}
	return v
}
