package media_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/media"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
)

// bucket is a fake S3 endpoint: PutObject and DeleteObject on path-style
// keys, and a key that refuses writes.
type bucket struct {
	mu      sync.Mutex
	objects map[string][]byte
	refuse  string
}

func (b *bucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/media-bucket/")
	b.mu.Lock()
	defer b.mu.Unlock()
	switch {
	case r.Method == http.MethodPut && b.refuse != "" && strings.HasSuffix(key, b.refuse):
		w.WriteHeader(http.StatusInternalServerError)
	case r.Method == http.MethodPut:
		b.objects[key], _ = io.ReadAll(r.Body)
	case r.Method == http.MethodDelete:
		delete(b.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func (b *bucket) keys() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.objects {
		out = append(out, k)
	}
	return out
}

func newStore(t *testing.T) (*media.Store, *bucket) {
	t.Helper()
	b := &bucket{objects: map[string][]byte{}}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	return media.NewStore(platform.Config{MediaS3Endpoint: srv.URL, MediaS3Region: "auto",
		MediaS3Bucket: "media-bucket", MediaS3AccessKey: "key", MediaS3SecretKey: "secret"}), b
}

// photo is a w×h JPEG carrying an EXIF segment with a GPS position, as a
// phone writes one.
func photo(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := range w {
		img.Set(x, h/2, color.RGBA{R: 200, A: 255})
	}
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, img, nil))
	exif := append([]byte("Exif\x00\x00"), []byte("GPSLatitude -33.8688 GPSLongitude 151.2093")...)
	segment := append([]byte{0xFF, 0xE1, 0, byte(len(exif) + 2)}, exif...)
	raw := buf.Bytes()
	return append(append(append([]byte{}, raw[:2]...), segment...), raw[2:]...)
}

func width(t *testing.T, b []byte) int {
	t.Helper()
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
	require.NoError(t, err)
	return cfg.Width
}

func requireCode(t *testing.T, code apperr.Code, err error) {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, e.Detail)
}

// An upload keeps a re-encoded original privately and three web sizes
// publicly, none of them carrying the photo's location.
func TestUploadStoresOriginalAndWebSizesWithoutMetadata(t *testing.T) {
	store, b := newStore(t)
	data := photo(t, 2000, 1000)
	require.Contains(t, string(data), "GPSLatitude")

	m, err := media.Upload(ctx, pgtest.Pool(t), store, data,
		media.Description{Alt: json.RawMessage(`{"en": "Red line"}`), Credit: "Daw Mi"}, pgtype.UUID{}, now)
	require.NoError(t, err)
	require.EqualValues(t, 2000, m.Width)
	require.EqualValues(t, 1000, m.Height)
	require.Equal(t, []int32{1600, 800, 400}, m.Widths)

	id := m.ID.String()
	require.ElementsMatch(t, []string{"originals/" + id + ".jpg", "public/" + id + "/1600.jpg",
		"public/" + id + "/800.jpg", "public/" + id + "/400.jpg"}, b.keys())
	for key, w := range map[string]int{"originals/" + id + ".jpg": 2000, "public/" + id + "/800.jpg": 800} {
		require.Equal(t, w, width(t, b.objects[key]))
	}
	for key, obj := range b.objects {
		require.NotContains(t, string(obj), "Exif", key)
		require.NotContains(t, string(obj), "GPS", key)
	}
	require.Equal(t, []media.Size{{Width: 1600, URL: "https://media.example/" + id + "/1600.jpg"},
		{Width: 800, URL: "https://media.example/" + id + "/800.jpg"}, {Width: 400, URL: "https://media.example/" + id + "/400.jpg"}},
		media.Sizes("https://media.example/", m.ID, m.Widths))
}

// A small image is never enlarged; transparency becomes white.
func TestSmallTransparentPNGKeepsItsSize(t *testing.T) {
	store, b := newStore(t)
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, 300, 200))))
	m, err := media.Upload(ctx, pgtest.Pool(t), store, buf.Bytes(), media.Description{}, pgtype.UUID{}, now)
	require.NoError(t, err)
	require.Equal(t, []int32{300}, m.Widths)
	img, err := jpeg.Decode(bytes.NewReader(b.objects["public/"+m.ID.String()+"/300.jpg"]))
	require.NoError(t, err)
	r, g, bl, _ := img.At(10, 10).RGBA()
	require.Greater(t, min(r, g, bl), uint32(0xf000), "white, not black")
}

func TestUploadRefusesWhatIsNotAnImage(t *testing.T) {
	store, b := newStore(t)
	pool := pgtest.Pool(t)
	_, err := media.Upload(ctx, pool, store, []byte("%PDF-1.7 not an image"), media.Description{}, pgtype.UUID{}, now)
	requireCode(t, apperr.UnsupportedMediaType, err)
	_, err = media.Upload(ctx, pool, store, make([]byte, media.MaxUploadBytes+1), media.Description{}, pgtype.UUID{}, now)
	requireCode(t, apperr.PayloadTooLarge, err)
	_, err = media.Upload(ctx, pool, nil, photo(t, 10, 10), media.Description{}, pgtype.UUID{}, now)
	requireCode(t, apperr.Unavailable, err)
	require.Empty(t, b.keys())
}

// A storage failure part-way leaves neither a row nor stray objects.
func TestFailedStorageLeavesNothingBehind(t *testing.T) {
	store, b := newStore(t)
	b.refuse = "/400.jpg"
	q := db.New(pgtest.Pool(t))
	before, err := media.List(ctx, q, media.Filter{Limit: 100})
	require.NoError(t, err)
	_, err = media.Upload(ctx, pgtest.Pool(t), store, photo(t, 900, 600), media.Description{}, pgtype.UUID{}, now)
	require.Error(t, err)
	after, err := media.List(ctx, q, media.Filter{Limit: 100})
	require.NoError(t, err)
	require.Len(t, after.Items, len(before.Items))
	require.Empty(t, b.keys())
}

// Alt text and credit are searchable and change only as of the version read.
func TestDescribeAndSearch(t *testing.T) {
	store, _ := newStore(t)
	q := db.New(pgtest.Pool(t))
	m, err := media.Upload(ctx, pgtest.Pool(t), store, photo(t, 500, 500), media.Description{}, pgtype.UUID{}, now)
	require.NoError(t, err)

	d := media.Description{Alt: json.RawMessage(`{"en": "Hands in clay", "my": "ရွှံ့"}`), Credit: "Studio photo"}
	described, err := media.Describe(ctx, q, m.ID, m.Version, d, now)
	require.NoError(t, err)
	require.Equal(t, "Studio photo", described.Credit.String)
	_, err = media.Describe(ctx, q, m.ID, m.Version, d, now)
	requireCode(t, apperr.StaleVersion, err)

	for _, search := range []string{"clay", "ရွှံ့", "studio"} {
		page, err := media.List(ctx, q, media.Filter{Search: search})
		require.NoError(t, err)
		require.Equal(t, m.ID, page.Items[0].ID, search)
	}
}

// An image a post uses, whatever the post's status, cannot be deleted.
func TestDeleteRefusesMediaInUse(t *testing.T) {
	store, b := newStore(t)
	pool := pgtest.Pool(t)
	m, err := media.Upload(ctx, pool, store, photo(t, 500, 500), media.Description{}, pgtype.UUID{}, now)
	require.NoError(t, err)
	post, err := content.CreatePost(ctx, pool, content.Edit{Title: "Clay", Kind: "insight",
		Versions: []db.SavePostVersionParams{{Channel: "instagram", ImageIds: []pgtype.UUID{m.ID}}}}, pgtype.UUID{}, now)
	require.NoError(t, err)

	requireCode(t, apperr.InUse, media.Delete(ctx, pool, store, m.ID))
	require.Len(t, b.keys(), 3)

	require.NoError(t, content.DeletePost(ctx, db.New(pool), post.ID))
	require.NoError(t, media.Delete(ctx, pool, store, m.ID))
	require.Empty(t, b.keys())
	requireCode(t, apperr.NotFound, media.Delete(ctx, pool, store, m.ID))
}
