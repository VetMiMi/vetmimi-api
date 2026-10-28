package content_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_PublicArticles(t *testing.T) {
	withCover := website("http-public-article")
	withCover.CoverImageID = newMedia(t)
	newPost(t, "insight", withCover).published()
	a := contentAPI(t)

	list := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/articles?locale=my&kind=insight", ""))
	require.Equal(t, "my", list["locale"])
	var found map[string]any
	for _, item := range list["items"].([]any) {
		if item.(map[string]any)["slug"] == "http-public-article" {
			found = item.(map[string]any)
		}
	}
	require.Equal(t, []string{"coverImage", "excerpt", "kind", "publishedAt", "slug", "title"}, keys(found))

	article := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/articles/http-public-article", ""))
	require.Equal(t, "# Calm\n\nBreathe.", article["body"])
	require.Equal(t, "Finding calm", article["seoTitle"], "the title stands in for a missing SEO title")
	cover := article["coverImage"].(map[string]any)
	require.Equal(t, "A blue painting", cover["alt"])
	require.Equal(t, map[string]any{"width": float64(1600), "url": "https://media.example.com/" +
		cover["id"].(string) + "/1600.jpg"}, cover["sizes"].([]any)[0])

	webtest.Refused(t, http.StatusNotFound, "not_found", a.Public(http.MethodGet, "/public/articles/no-such-article", ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Public(http.MethodGet, "/public/articles?kind=poem", ""))
	require.Equal(t, "kind", p["errors"].([]any)[0].(map[string]any)["field"])
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, "/public/articles", ""))
}
