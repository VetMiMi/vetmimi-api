package content_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func slugs(articles []content.Article) []string {
	out := make([]string, len(articles))
	for i, a := range articles {
		out[i] = a.Slug
	}
	return out
}

// Visitors see website versions of published posts only, in their locale
// with English as the fallback; approved, archived or social-only posts
// never show.
func TestPublicArticlesAreOnlyPublishedWebsiteVersions(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	withCover := website("live-both-languages")
	withCover.CoverImageID = newMedia(t)
	newPost(t, "insight", withCover).published()
	englishOnly := website("live-english-only")
	englishOnly.Title, englishOnly.Body = localized("Only English", ""), localized("Body", "")
	story := newPost(t, "true_story", englishOnly)
	require.NoError(t, story.edit(content.Edit{Consent: &content.Consent{Confirmed: true}}))
	story.published()

	approved := newPost(t, "insight", website("approved-not-live"))
	require.NoError(t, approved.submit())
	require.NoError(t, approved.approve())
	archived := newPost(t, "insight", website("archived-article")).published()
	_, err := content.Archive(ctx, pgtest.Pool(t), archived.post.ID, archived.post.Version, archived.now)
	require.NoError(t, err)
	hidden := website("website-switched-off")
	hidden.Enabled = false
	newPost(t, "insight", hidden, facebook("Facebook only")).published()

	all, err := content.ListArticles(ctx, q, "my", "", 100)
	require.NoError(t, err)
	got := slugs(all)
	require.Subset(t, got, []string{"live-both-languages", "live-english-only"})
	for _, absent := range []string{"approved-not-live", "archived-article", "website-switched-off"} {
		require.NotContains(t, got, absent)
	}
	stories, err := content.ListArticles(ctx, q, "en", "true_story", 100)
	require.NoError(t, err)
	require.Contains(t, slugs(stories), "live-english-only")
	require.NotContains(t, slugs(stories), "live-both-languages")

	burmese, err := content.GetArticle(ctx, q, "live-both-languages", "my")
	require.NoError(t, err)
	require.Equal(t, "ငြိမ်သက်မှု", burmese.Title)
	require.Equal(t, "# ငြိမ်\n\nအသက်ရှူပါ။", burmese.Body)
	require.Equal(t, "A short read.", burmese.Excerpt, "no Burmese excerpt, so English")
	require.Equal(t, burmese.Title, burmese.SEOTitle)
	require.Equal(t, &content.Cover{ID: withCover.CoverImageID, Width: 1600, Height: 900,
		Widths: []int32{1600, 800, 400}, Alt: "အပြာရောင်ပန်းချီ"}, burmese.Cover)
	english, err := content.GetArticle(ctx, q, "live-both-languages", "en")
	require.NoError(t, err)
	require.Equal(t, "Finding calm", english.Title)
	require.Equal(t, "A blue painting", english.Cover.Alt)
	fallback, err := content.GetArticle(ctx, q, "live-english-only", "my")
	require.NoError(t, err)
	require.Equal(t, "Only English", fallback.Title)
	require.Nil(t, fallback.Cover)

	for _, slug := range []string{"approved-not-live", "archived-article", "website-switched-off"} {
		_, err := content.GetArticle(ctx, q, slug, "en")
		requireCode(t, apperr.NotFound, err)
	}
}
