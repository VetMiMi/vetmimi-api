package content_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func contentAPI(t *testing.T) *webtest.API {
	h := &content.Handler{Pool: pgtest.Pool(t), MediaPublicURL: "https://media.example.com", Now: webtest.Clock}
	return webtest.New(t, h.Routes)
}

func keys(v any) []string {
	return slices.Sorted(maps.Keys(v.(map[string]any)))
}

const newPostBody = `{"title": "Calm", "kind": "insight", "versions": {
	"website": {"enabled": true, "slug": "http-calm", "title": {"en": "Calm"}, "body": {"en": "Breathe."}},
	"facebook": {"enabled": true, "text": ""}}}`

func TestHTTP_Posts(t *testing.T) {
	a := contentAPI(t)
	editor := webtest.SignIn(t, "content_editor")

	post := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, "/admin/posts", editor, newPostBody))
	require.Equal(t, []string{"authorId", "consent", "createdAt", "id", "kind", "publications", "status", "title",
		"updatedAt", "version", "versions"}, keys(post))
	require.Equal(t, "draft", post["status"])
	require.Equal(t, []any{}, post["publications"])
	require.Equal(t, map[string]any{"confirmed": false}, post["consent"])
	versions := post["versions"].(map[string]any)
	require.Equal(t, map[string]any{"enabled": true, "slug": "http-calm", "title": map[string]any{"en": "Calm"},
		"body": map[string]any{"en": "Breathe."}}, versions["website"])
	require.Equal(t, map[string]any{"enabled": true, "text": "", "imageIds": []any{}}, versions["facebook"],
		"an empty text is kept apart from a missing one")

	path := "/admin/posts/" + post["id"].(string)
	got := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, path, editor, ""))
	require.Equal(t, post, got)
	saved := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPatch, path, editor, `{"version": 1, "title": "Calmer"}`))
	require.Equal(t, "Calmer", saved["title"])

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/posts?q=Calmer&limit=1", editor, ""))
	require.Equal(t, path, "/admin/posts/"+list["items"].([]any)[0].(map[string]any)["id"].(string))

	require.Equal(t, http.StatusNoContent, a.Admin(http.MethodDelete, path, editor, "").Code)
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodGet, path, editor, ""))
}

func TestHTTP_PostsRefused(t *testing.T) {
	a := contentAPI(t)
	editor := webtest.SignIn(t, "content_editor")

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPost, "/admin/posts", webtest.SignIn(t, "booking_admin"), newPostBody))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, "/admin/posts", editor,
		`{"title": "Calm", "kind": "insight", "versions": {"website": {"enabled": true, "slug": "Not A Slug"}}}`))
	require.Equal(t, []any{map[string]any{"field": "/versions/website/slug",
		"message": "must be lowercase words joined by hyphens"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/posts/42", editor, ""))
	require.Equal(t, []any{map[string]any{"field": "postId", "message": "must be a valid uuid"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/posts?limit=0", editor, ""))
	require.Equal(t, []any{map[string]any{"field": "limit", "message": "must be at least 1"}}, p["errors"])
}
