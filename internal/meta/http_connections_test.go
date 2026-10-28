package meta_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_ConnectThePage(t *testing.T) {
	c, g := newConnector(t)
	c.Now = webtest.Clock
	login(g)
	g.on("GET me/accounts", http.StatusOK, onePage)
	h := &meta.Handler{Connector: c}
	a := webtest.New(t, h.Routes)
	admin := webtest.SignIn(t, "site_admin")

	started := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, "/admin/connections/meta/authorize", admin, ""))
	link, err := url.Parse(started["authorizeUrl"].(string))
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": link.Query().Get("state")})

	conn := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, "/admin/connections/meta/callback", admin, string(body)))
	require.Equal(t, "connected", conn["status"])
	require.Equal(t, "vetmimi", conn["instagramUsername"])
	require.NotContains(t, conn, "pages")

	require.Equal(t, http.StatusNoContent, a.Admin(http.MethodDelete, "/admin/connections/meta", admin, "").Code)
	status := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/connections/meta", admin, ""))
	require.Equal(t, map[string]any{"status": "not_connected"}, status)
	require.NotContains(t, a.Logs.String(), pageToken)
}

func TestHTTP_ConnectThePageRefused(t *testing.T) {
	c, _ := newConnector(t)
	a := webtest.New(t, (&meta.Handler{Connector: c}).Routes)
	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPost, "/admin/connections/meta/authorize", webtest.SignIn(t, "content_editor"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPut, "/admin/connections/meta/page", webtest.SignIn(t, "site_admin"), `{}`))
	require.Equal(t, []any{map[string]any{"field": "/pageId", "message": "is required"}}, p["errors"])
}
