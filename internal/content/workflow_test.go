package content_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// draft → scheduled → draft → publishing → published once Daw Mi marks
// Facebook posted by hand.
func TestWorkflowFromDraftToPublished(t *testing.T) {
	f := newPost(t, "insight", website("whole-path"), facebook("New article on the site."))
	pool := pgtest.Pool(t)

	err := f.schedule(f.now)
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/scheduledAt", e.Fields[0].Field)
	require.NoError(t, f.schedule(f.now.Add(24*time.Hour)))
	require.Equal(t, "scheduled", f.post.Status)
	require.Equal(t, f.by, f.post.ApprovedBy)
	p, err := content.Unschedule(ctx, pool, f.post.ID, f.post.Version, f.now)
	require.NoError(t, err)
	require.Equal(t, "draft", p.Status)
	require.False(t, p.ScheduledAt.Valid)
	f.post = p

	require.NoError(t, f.publish())
	require.Equal(t, "publishing", f.post.Status)
	require.Equal(t, "published", publication(f.post, "website").Status)
	require.Equal(t, "pending", publication(f.post, "facebook").Status)
	require.False(t, f.post.PublishedAt.Valid)

	_, err = content.MarkPosted(ctx, pool, f.post.ID, "instagram", "", f.now)
	requireCode(t, apperr.ActionNotAllowed, err)
	p, err = content.MarkPosted(ctx, pool, f.post.ID, "facebook", "https://www.facebook.com/vetmimi/posts/1", f.now)
	require.NoError(t, err)
	require.Equal(t, "published", p.Status)
	require.True(t, p.PublishedAt.Valid)
	fb := publication(p, "facebook")
	require.Equal(t, "manual", fb.Status)
	require.Equal(t, "https://www.facebook.com/vetmimi/posts/1", fb.Permalink.String)
}

func TestActionsRefuseOtherStatusesAndStaleVersions(t *testing.T) {
	pool := pgtest.Pool(t)
	f := newPost(t, "announcement", facebook("Studio closed on Monday."))

	_, err := content.Unschedule(ctx, pool, f.post.ID, f.post.Version, f.now)
	requireCode(t, apperr.InvalidTransition, err)
	_, err = content.PublishNow(ctx, pool, f.post.ID, f.post.Version+1, f.by, f.now)
	requireCode(t, apperr.StaleVersion, err)

	f.published()
	require.Equal(t, "publishing", f.post.Status)
	requireCode(t, apperr.InvalidTransition, f.publish())
	requireCode(t, apperr.ActionNotAllowed, f.edit(content.Edit{Versions: []db.SavePostVersionParams{facebook("Too late")}}))
	p, err := content.Archive(ctx, pool, f.post.ID, f.post.Version, f.now)
	require.NoError(t, err)
	require.Equal(t, "archived", p.Status)
	_, err = content.Archive(ctx, pool, f.post.ID, p.Version, f.now)
	requireCode(t, apperr.InvalidTransition, err)
}

func TestEditingAScheduledPostSendsItBackToDraft(t *testing.T) {
	f := newPost(t, "insight", website("edit-after-scheduling"))
	require.NoError(t, f.schedule(f.now.Add(time.Hour)))
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{facebook("Also on Facebook")}}))
	require.Equal(t, "draft", f.post.Status)
	require.False(t, f.post.ScheduledAt.Valid)
	require.False(t, f.post.ApprovedAt.Valid)

	f = newPost(t, "insight")
	require.NoError(t, f.edit(content.Edit{Status: "idea"}))
	require.Equal(t, "idea", f.post.Status)
	require.NoError(t, f.edit(content.Edit{Title: "Still an idea"}))
	require.Equal(t, "idea", f.post.Status)
}

func TestPublishChecksEveryEnabledChannel(t *testing.T) {
	ig := db.SavePostVersionParams{Channel: "instagram", Enabled: true,
		Text: textOf(strings.Repeat("calm ", 441) + strings.Repeat("#art ", 31))}
	li := db.SavePostVersionParams{Channel: "linkedin", Enabled: true, Text: textOf(strings.Repeat("x", 3001))}
	site := website("checked-site")
	site.Excerpt = localized("", "မြန်မာ")
	site.CoverImageID = randomIDs(1)[0]
	off := db.SavePostVersionParams{Channel: "facebook", Enabled: false, ImageIds: randomIDs(1)}
	f := newPost(t, "insight", site, ig, li, off)

	e := requireCode(t, apperr.PublishRequirementsUnmet, f.publish())
	require.ElementsMatch(t, []apperr.FieldError{
		{Field: "/versions/website/excerpt/en", Message: "is required"},
		{Field: "/versions/website/coverImageId", Message: "is not in the media library"},
		{Field: "/versions/instagram/imageIds", Message: "needs at least one image"},
		{Field: "/versions/instagram/caption", Message: "must be at most 2,200 characters"},
		{Field: "/versions/instagram/caption", Message: "must have at most 30 hashtags"},
		{Field: "/versions/linkedin/text", Message: "must be at most 3,000 characters"},
	}, e.Fields)
	require.Equal(t, "draft", f.post.Status)
	requireCode(t, apperr.PublishRequirementsUnmet, f.schedule(f.now.Add(time.Hour)))

	ig.Text, ig.ImageIds = textOf("Calm, in colour. #arttherapy"), []pgtype.UUID{newMedia(t), randomIDs(1)[0]}
	li.Text = textOf("A new article on finding calm.")
	site = website("checked-site")
	site.CoverImageID = newMedia(t)
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{site, ig, li}}))
	e = requireCode(t, apperr.PublishRequirementsUnmet, f.publish())
	require.Equal(t, []apperr.FieldError{{Field: "/versions/instagram/imageIds/1", Message: "is not in the media library"}},
		e.Fields)

	ig.ImageIds[1] = newMedia(t)
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{ig}}))
	require.NoError(t, f.schedule(f.now.Add(time.Hour)))
}

func TestPublishNeedsAnEnabledChannel(t *testing.T) {
	f := newPost(t, "insight")
	e := requireCode(t, apperr.PublishRequirementsUnmet, f.publish())
	require.Equal(t, []apperr.FieldError{{Field: "/versions", Message: "needs at least one enabled channel"}}, e.Fields)
}

func TestTrueStoryNeedsConsentBeforePublishing(t *testing.T) {
	f := newPost(t, "true_story", facebook("A story shared with permission."))
	e := requireCode(t, apperr.PublishRequirementsUnmet, f.publish())
	require.Equal(t, []apperr.FieldError{{Field: "/consent/confirmed", Message: "must be confirmed for a True Story"}}, e.Fields)

	require.NoError(t, f.edit(content.Edit{Consent: &content.Consent{Confirmed: true, Note: "Signed form in Drive"}}))
	require.True(t, f.post.ConsentConfirmedAt.Valid)
	require.Equal(t, f.by, f.post.ConsentConfirmedBy)
	require.NoError(t, f.publish())
}
