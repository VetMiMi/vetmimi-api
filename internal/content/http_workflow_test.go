package content_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

// An editor submits; only a site administrator approves and publishes; a
// channel posted by hand completes the post.
func TestHTTP_Workflow(t *testing.T) {
	a := contentAPI(t)
	editor := webtest.SignIn(t, "content_editor")
	admin := webtest.SignIn(t, "site_admin")
	post := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, "/admin/posts", editor,
		`{"title": "Calm", "kind": "insight", "versions": {
			"website": {"enabled": true, "slug": "http-workflow", "title": {"en": "Calm"},
				"excerpt": {"en": "Short."}, "body": {"en": "Breathe."}},
			"facebook": {"enabled": true, "text": "New article."}}}`))
	path := "/admin/posts/" + post["id"].(string)

	webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/submit", editor, `{"version": 1}`))
	webtest.Refused(t, http.StatusForbidden, "forbidden", a.Admin(http.MethodPost, path+"/approve", editor, `{"version": 2}`))
	approved := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/approve", admin, `{"version": 2}`))
	require.Equal(t, "approved", approved["status"])
	require.NotEmpty(t, approved["approvedBy"])

	publishing := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/publish", admin, `{"version": 3}`))
	require.Equal(t, "publishing", publishing["status"], "facebook is left to the worker")

	webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPost, path+"/channels/website/mark-posted", editor, `{}`))
	webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodPost, path+"/channels//retry", editor, ``))
	posted := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/channels/facebook/mark-posted", editor,
		`{"permalink": "https://facebook.com/p/1"}`))
	require.Equal(t, "published", posted["status"])
	webtest.Refused(t, http.StatusConflict, "stale_version", a.Admin(http.MethodPost, path+"/archive", admin, `{"version": 1}`))
}
