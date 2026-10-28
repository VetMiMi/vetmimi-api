package media_test

import (
	"bytes"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/web"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func mediaAPI(t *testing.T) *webtest.API {
	store, _ := newStore(t)
	h := &media.Handler{Pool: pgtest.Pool(t), Store: store, PublicURL: "https://media.example", Now: webtest.Clock}
	return webtest.New(t, func(r web.Routes) { h.Routes(r) })
}

func upload(t *testing.T, a *webtest.API, token string, file []byte, fields ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for i := 0; i+1 < len(fields); i += 2 {
		require.NoError(t, form.WriteField(fields[i], fields[i+1]))
	}
	part, err := form.CreateFormFile("file", "photo.jpg")
	require.NoError(t, err)
	_, err = part.Write(file)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	req := httptest.NewRequest(http.MethodPost, "/admin/media", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	res := httptest.NewRecorder()
	a.Router.ServeHTTP(res, req)
	return res
}

func TestHTTP_Media(t *testing.T) {
	a := mediaAPI(t)
	editor := webtest.SignIn(t, "content_editor")

	m := webtest.JSON(t, http.StatusCreated, upload(t, a, editor, photo(t, 800, 400), "altEn", "Red line", "credit", "Daw Mi"))
	id := m["id"].(string)
	require.Equal(t, []string{"alt", "byteSize", "createdAt", "credit", "height", "id", "sizes", "updatedAt",
		"uploadedBy", "version", "width"}, slices.Sorted(maps.Keys(m)))
	require.Equal(t, map[string]any{"en": "Red line"}, m["alt"])
	require.Equal(t, map[string]any{"width": float64(800), "url": "https://media.example/" + id + "/800.jpg"},
		m["sizes"].([]any)[0])

	m = webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPatch, "/admin/media/"+id, editor,
		`{"version": 1, "alt": {"my": "ရွှံ့"}}`))
	require.Equal(t, map[string]any{"my": "ရွှံ့"}, m["alt"])
	require.Nil(t, m["credit"], "a credit left out is cleared")

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/media?limit=1&q="+url.QueryEscape("ရွှံ့"), editor, ""))
	require.Equal(t, id, list["items"].([]any)[0].(map[string]any)["id"])

	require.Equal(t, http.StatusNoContent, a.Admin(http.MethodDelete, "/admin/media/"+id, editor, "").Code)
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodGet, "/admin/media/"+id, editor, ""))
}

func TestHTTP_MediaRefusals(t *testing.T) {
	a := mediaAPI(t)
	editor := webtest.SignIn(t, "content_editor")

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodGet, "/admin/media", webtest.SignIn(t, "booking_admin"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/media/nope", editor, ""))
	require.Equal(t, []any{map[string]any{"field": "mediaId", "message": "must be a valid uuid"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/media?limit=0", editor, ""))
	require.Equal(t, []any{map[string]any{"field": "limit", "message": "must be at least 1"}}, p["errors"])
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/media?limit=abc", editor, ""))
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPatch,
		"/admin/media/6f1c2c1e-4d7a-4a51-9f0e-0c6f2d9f8b11", editor, `{"version": 1, "alt": {"en": "`+strings.Repeat("x", 301)+`"}}`))
	require.Equal(t, []any{map[string]any{"field": "/alt/en", "message": "must be at most 300 characters"}}, p["errors"])
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", upload(t, a, editor, photo(t, 10, 10), "location", "Sydney"))
}
