package httpapi

import (
	"bytes"
	"image"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// acceptingBucket is an S3 endpoint that stores nothing and accepts every
// write and delete; internal/media tests what is stored.
func acceptingBucket(t *testing.T) *media.Store {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)
	return media.NewStore(platform.Config{MediaS3Endpoint: srv.URL, MediaS3Region: "auto",
		MediaS3Bucket: "media", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"})
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil))
	return buf.Bytes()
}

// upload posts file and the form fields to the upload route.
func (a *authAPI) upload(t *testing.T, token string, file []byte, fields ...string) *httptest.ResponseRecorder {
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
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", form.FormDataContentType())
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

// The upload route keeps the signed-in checks of every other route, under
// its own 21 MiB cap.
func TestMediaUploadThroughTheRouter(t *testing.T) {
	a := newAuthAPI(t)
	editor := insertSession(t, a.clock.at, "content_editor")

	m := decoded(t, http.StatusCreated, a.upload(t, editor, jpegOf(t, 1200, 800), "altEn", "Clay on a wheel", "credit", "Daw Mi"))
	id := m["id"].(string)
	require.Equal(t, map[string]any{"en": "Clay on a wheel"}, m["alt"])
	require.Equal(t, []any{
		map[string]any{"width": float64(1200), "url": "https://media.vetmimi.example/" + id + "/1200.jpg"},
		map[string]any{"width": float64(800), "url": "https://media.vetmimi.example/" + id + "/800.jpg"},
		map[string]any{"width": float64(400), "url": "https://media.vetmimi.example/" + id + "/400.jpg"},
	}, m["sizes"])

	requireForbidden(t, a.upload(t, insertSession(t, a.clock.at, "booking_admin"), jpegOf(t, 10, 10)))
	res := a.upload(t, "", jpegOf(t, 10, 10))
	require.Equal(t, http.StatusUnauthorized, res.Code, res.Body.String())
	refused(t, http.StatusUnsupportedMediaType, "unsupported_media_type", a.upload(t, editor, []byte("not an image")))
	refused(t, http.StatusRequestEntityTooLarge, "payload_too_large", a.upload(t, editor, make([]byte, 22<<20)))
	refused(t, http.StatusBadRequest, "invalid_request", a.upload(t, editor, jpegOf(t, 10, 10), "location", "Sydney"))

	patch := `{"version": 1, "alt": {"en": "Hands in clay", "my": "ရွှံ့"}}`
	m = decoded(t, http.StatusOK, a.sendJSON(http.MethodPatch, "/admin/media/"+id, editor, patch))
	require.Nil(t, m["credit"], "replaced whole")
	listed := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/media?q=clay", editor))
	require.Equal(t, id, listed["items"].([]any)[0].(map[string]any)["id"])
	a.send(http.MethodDelete, "/admin/media/"+id, editor)
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodGet, "/admin/media/"+id, editor))
}

func TestUploadMediaIsIndexedAsDeclared(t *testing.T) {
	ops := index(t, load(t, "../../openapi.yaml"))
	require.Equal(t, uploadMediaOperation, ops[operationKey(http.MethodPost, uploadMediaPattern)])
}
