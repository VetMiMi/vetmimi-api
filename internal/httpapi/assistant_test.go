package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeClaude answers every Messages API call with a LinkedIn suggestion.
func fakeClaude(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"stop_reason": "end_turn", "content": [{"type": "text",
			"text": "{\"linkedin\": {\"text\": \"Reflecting on calm.\"}}"}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// An editor asks for suggestions, which change nothing in the post; a
// booking administrator may not; each user has 20 an hour; without a key
// the assistant is off.
func TestSuggestPostVersions(t *testing.T) {
	a := newAuthAPI(t)
	a.assistant.URL = fakeClaude(t)
	editor := insertSession(t, a.clock.at, "content_editor")
	post := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, "/admin/posts", editor,
		`{"title": "Calm", "kind": "insight", "versions": {"website": {"enabled": true,
			"title": {"en": "Calm"}, "body": {"en": "Painting slowly helps me breathe."}}}}`))
	path := "/admin/posts/" + post["id"].(string) + "/suggestions"
	ask := `{"channels": ["linkedin"]}`

	require.Equal(t, true, decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/ai/status", editor))["enabled"])
	got := decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path, editor, ask))
	require.Equal(t, map[string]any{"linkedin": map[string]any{"text": "Reflecting on calm."}}, got)
	after := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/posts/"+post["id"].(string), editor))
	require.Equal(t, post["version"], after["version"], "nothing saved")

	requireForbidden(t, a.sendJSON(http.MethodPost, path, insertSession(t, a.clock.at, "booking_admin"), ask))
	refused(t, http.StatusBadRequest, "invalid_request", a.sendJSON(http.MethodPost, path, editor, `{"channels": []}`))

	other := insertSession(t, a.clock.at, "content_editor")
	for range 20 {
		require.Equal(t, http.StatusOK, a.sendJSON(http.MethodPost, path, other, ask).Code)
	}
	refused(t, http.StatusTooManyRequests, "rate_limited", a.sendJSON(http.MethodPost, path, other, ask))

	a.assistant.APIKey = ""
	require.Equal(t, false, decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/ai/status", editor))["enabled"])
	refused(t, http.StatusServiceUnavailable, "feature_unavailable", a.sendJSON(http.MethodPost, path, editor, ask))
}
