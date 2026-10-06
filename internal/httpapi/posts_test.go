package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// action posts {"version": v} to one of a post's workflow actions.
func (a *authAPI) action(t *testing.T, token, id, name string, post map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return a.sendJSON(http.MethodPost, "/admin/posts/"+id+"/"+name, token,
		fmt.Sprintf(`{"version": %v}`, post["version"]))
}

// Through the router: an editor writes and submits, only a site
// administrator approves and publishes, and the article then reaches the
// public read in the visitor's language.
func TestPostFromEditorToPublicArticle(t *testing.T) {
	a := newAuthAPI(t)
	editor := insertSession(t, a.clock.at, "content_editor")
	admin := insertSession(t, a.clock.at, "site_admin")
	image := decoded(t, http.StatusCreated, a.upload(t, editor, jpegOf(t, 900, 600),
		"altEn", "A blue painting", "altMy", "အပြာရောင်ပန်းချီ"))["id"].(string)

	long := strings.Repeat("ကြည့်", 15000) // 225 KB of Burmese, over the 64 KiB default cap
	body := fmt.Sprintf(`{"title": "Colour and calm", "kind": "insight", "versions": {
		"website": {"enabled": true, "slug": "colour-and-calm",
			"title": {"en": "Colour and calm", "my": "အရောင်နှင့် ငြိမ်သက်မှု"},
			"excerpt": {"en": "How colour settles the mind."},
			"body": {"en": "# Colour\n\nPaint slowly.", "my": %q}, "coverImageId": %q},
		"instagram": {"enabled": true, "caption": "Colour and calm #arttherapy"}}}`, long, image)
	post := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, "/admin/posts", editor, body))
	require.Equal(t, "draft", post["status"])
	id := post["id"].(string)

	post = decoded(t, http.StatusOK, a.action(t, editor, id, "submit", post))
	require.Equal(t, "in_review", post["status"])
	requireForbidden(t, a.action(t, editor, id, "approve", post))
	requireForbidden(t, a.action(t, insertSession(t, a.clock.at, "booking_admin"), id, "approve", post))

	problem := refused(t, http.StatusUnprocessableEntity, "publish_requirements_unmet", a.action(t, admin, id, "approve", post))
	require.Equal(t, []any{map[string]any{"field": "/versions/instagram/imageIds", "message": "needs at least one image"}},
		problem["errors"])

	patch := fmt.Sprintf(`{"version": %v, "versions": {"instagram": {"enabled": true,
		"caption": "Colour and calm #arttherapy", "imageIds": [%q]}}}`, post["version"], image)
	post = decoded(t, http.StatusOK, a.sendJSON(http.MethodPatch, "/admin/posts/"+id, editor, patch))
	refused(t, http.StatusConflict, "in_use", a.send(http.MethodDelete, "/admin/media/"+image, editor))
	post = decoded(t, http.StatusOK, a.action(t, admin, id, "approve", post))
	require.Equal(t, "approved", post["status"])
	requireForbidden(t, a.action(t, editor, id, "publish", post))

	post = decoded(t, http.StatusOK, a.action(t, admin, id, "publish", post))
	require.Equal(t, "publishing", post["status"])
	summary := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/posts?q=colour", editor))["items"].([]any)[0].(map[string]any)
	require.Equal(t, []any{map[string]any{"channel": "instagram", "status": "pending"},
		map[string]any{"channel": "website", "status": "published"}}, summary["publications"])
	require.NotContains(t, summary, "publishedAt", "not every channel is out yet")
	post = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, "/admin/posts/"+id+"/channels/instagram/mark-posted",
		editor, `{"permalink": "https://www.instagram.com/p/abc/"}`))
	require.Equal(t, "published", post["status"])

	article := decoded(t, http.StatusOK, a.sendPublic(http.MethodGet, "/public/articles/colour-and-calm?locale=my", ""))
	require.Equal(t, "အရောင်နှင့် ငြိမ်သက်မှု", article["title"])
	require.Equal(t, long, article["body"])
	require.Equal(t, "How colour settles the mind.", article["excerpt"])
	cover := article["coverImage"].(map[string]any)
	require.Equal(t, "အပြာရောင်ပန်းချီ", cover["alt"])
	require.Equal(t, "https://media.vetmimi.example/"+image+"/400.jpg", cover["sizes"].([]any)[2].(map[string]any)["url"])
	list := decoded(t, http.StatusOK, a.sendPublic(http.MethodGet, "/public/articles", ""))
	require.Equal(t, "en", list["locale"])
	item := list["items"].([]any)[0].(map[string]any)
	require.Equal(t, "colour-and-calm", item["slug"])
	require.Equal(t, "A blue painting", item["coverImage"].(map[string]any)["alt"])

	refused(t, http.StatusNotFound, "not_found", a.sendPublic(http.MethodGet, "/public/articles/no-such-article", ""))
	listed := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/posts?status=published&q=colour", editor))
	summary = listed["items"].([]any)[0].(map[string]any)
	require.Equal(t, []any{"instagram", "website"}, summary["channels"])
	require.Equal(t, "manual", summary["publications"].([]any)[0].(map[string]any)["status"])
	require.Equal(t, "2026-10-05T09:30:15Z", summary["publishedAt"])
}
