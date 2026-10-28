package linkedin_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_ConnectLinkedIn(t *testing.T) {
	c, f := newConnector(t)
	signIn(f, "openid,profile,w_member_social")
	a := webtest.New(t, (&linkedin.Handler{Connector: c, Now: webtest.Clock}).Routes)
	admin := webtest.SignIn(t, "site_admin")

	started := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, "/admin/connections/linkedin/authorize", admin, ""))
	link, err := url.Parse(started["authorizeUrl"].(string))
	require.NoError(t, err)
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": link.Query().Get("state")})

	conn := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, "/admin/connections/linkedin/callback", admin, string(body)))
	require.Equal(t, "connected", conn["status"])
	require.Equal(t, "Daw Mi", conn["memberName"])
	require.Equal(t, "2026-12-04T09:30:15Z", conn["expiresAt"], "60 days on, in UTC")

	require.Equal(t, http.StatusNoContent, a.Admin(http.MethodDelete, "/admin/connections/linkedin", admin, "").Code)
	status := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/connections/linkedin", admin, ""))
	require.Equal(t, map[string]any{"status": "not_connected"}, status)
	require.NotContains(t, a.Logs.String(), memberToken)
}

func TestHTTP_ConnectLinkedInRefused(t *testing.T) {
	c, _ := newConnector(t)
	a := webtest.New(t, (&linkedin.Handler{Connector: c, Now: webtest.Clock}).Routes)
	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodGet, "/admin/connections/linkedin", webtest.SignIn(t, "content_editor"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPost, "/admin/connections/linkedin/callback", webtest.SignIn(t, "site_admin"), `{"code": "c"}`))
	require.Equal(t, []any{map[string]any{"field": "/state", "message": "is required"}}, p["errors"])
}
