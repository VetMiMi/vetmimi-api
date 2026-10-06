package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeGraph answers the Graph API calls Facebook Login makes, for one Page.
func fakeGraph(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.TrimPrefix(r.URL.Path, "/v24.0/") {
		case "oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token": "user-token", "expires_in": 5184000}`))
		case "me/accounts":
			_, _ = w.Write([]byte(`{"data": [{"id": "page-1", "name": "VetMiMi", "access_token": "page-token-secret",
				"instagram_business_account": {"id": "ig-1", "username": "vetmimi"}}]}`))
		case "debug_token":
			_, _ = w.Write([]byte(`{"data": {"expires_at": 0}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Only a site administrator connects the Page: start Facebook Login, finish
// it with the code and state Facebook sent back, read the connection, and
// disconnect. The token never reaches a response or a log.
func TestConnectTheFacebookPage(t *testing.T) {
	a := newAuthAPI(t)
	a.meta.GraphURL = fakeGraph(t)
	admin := insertSession(t, a.clock.at, "site_admin")
	requireForbidden(t, a.send(http.MethodPost, "/admin/connections/meta/authorize",
		insertSession(t, a.clock.at, "content_editor")))

	status := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/connections/meta", admin))
	require.Equal(t, "not_connected", status["status"])

	started := decoded(t, http.StatusOK, a.send(http.MethodPost, "/admin/connections/meta/authorize", admin))
	link, err := url.Parse(started["authorizeUrl"].(string))
	require.NoError(t, err)
	state := link.Query().Get("state")

	other := insertSession(t, a.clock.at, "site_admin")
	body := func(code string) string {
		b, _ := json.Marshal(map[string]string{"code": code, "state": state})
		return string(b)
	}
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendJSON(http.MethodPost, "/admin/connections/meta/callback", other, body("code-1")))
	conn := decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, "/admin/connections/meta/callback", admin, body("code-1")))
	require.Equal(t, "connected", conn["status"])
	require.Equal(t, "VetMiMi", conn["pageName"])
	require.Equal(t, "vetmimi", conn["instagramUsername"])
	require.NotContains(t, conn, "expiresAt", "a Page token that never expires")

	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendJSON(http.MethodPut, "/admin/connections/meta/page", admin, `{"pageId": "page-1"}`))
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, "/admin/connections/meta", admin).Code)
	status = decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/connections/meta", admin))
	require.Equal(t, "not_connected", status["status"])
	require.NotContains(t, a.logs.String(), "page-token-secret")
	require.NotContains(t, a.logs.String(), "user-token")
}

// Without the Meta app configured, starting is 503 unavailable.
func TestMetaNotSetUp(t *testing.T) {
	a := newAuthAPI(t)
	a.meta.AppID = ""
	refused(t, http.StatusServiceUnavailable, "unavailable", a.send(http.MethodPost,
		"/admin/connections/meta/authorize", insertSession(t, a.clock.at, "site_admin")))
}
