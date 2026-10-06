package content_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// The whole path: draft → in_review → approved → scheduled → approved →
// publishing (the website live, Facebook pending) → published once Daw Mi
// marks Facebook posted by hand.
func TestWorkflowFromDraftToPublished(t *testing.T) {
	f := newPost(t, "insight", website("whole-path"), facebook("New article on the site."))
	pool := pgtest.Pool(t)

	require.NoError(t, f.submit())
	require.Equal(t, "in_review", f.post.Status)
	require.NoError(t, f.approve())
	require.Equal(t, "approved", f.post.Status)
	require.Equal(t, f.by, f.post.ApprovedBy)

	err := f.schedule(f.now)
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/scheduledAt", e.Fields[0].Field)
	require.NoError(t, f.schedule(f.now.Add(24*time.Hour)))
	require.Equal(t, "scheduled", f.post.Status)
	p, err := content.Unschedule(ctx, pool, f.post.ID, f.post.Version, f.now)
	require.NoError(t, err)
	require.Equal(t, "approved", p.Status)
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

// Each action starts only from its own statuses, and only as of the version
// the caller read.
func TestActionsRefuseOtherStatusesAndStaleVersions(t *testing.T) {
	pool := pgtest.Pool(t)
	f := newPost(t, "announcement", facebook("Studio closed on Monday."))

	requireCode(t, apperr.InvalidTransition, f.approve())
	requireCode(t, apperr.InvalidTransition, f.publish())
	_, err := content.Submit(ctx, pool, f.post.ID, f.post.Version+1, f.now)
	requireCode(t, apperr.StaleVersion, err)

	require.NoError(t, f.submit())
	requireCode(t, apperr.InvalidTransition, f.submit())
	p, err := content.RequestChanges(ctx, pool, f.post.ID, f.post.Version, "Shorter, please.", f.now)
	require.NoError(t, err)
	require.Equal(t, "draft", p.Status)
	require.Equal(t, "Shorter, please.", p.ReviewNote.String)
	f.post = p

	f.published()
	require.Equal(t, "publishing", f.post.Status)
	requireCode(t, apperr.InvalidTransition, f.edit(content.Edit{Title: "Too late"}))
	p, err = content.Archive(ctx, pool, f.post.ID, f.post.Version, f.now)
	require.NoError(t, err)
	require.Equal(t, "archived", p.Status)
	_, err = content.Archive(ctx, pool, f.post.ID, p.Version, f.now)
	requireCode(t, apperr.InvalidTransition, err)
}

// Saving an approved or scheduled post withdraws the approval, so nothing
// unreviewed goes out.
func TestEditingAnApprovedPostSendsItBackToReview(t *testing.T) {
	f := newPost(t, "insight", website("edit-after-approval"))
	require.NoError(t, f.submit())
	require.NoError(t, f.approve())
	require.NoError(t, f.edit(content.Edit{Title: "Finding calm, again"}))
	require.Equal(t, "in_review", f.post.Status)
	require.False(t, f.post.ApprovedAt.Valid)

	require.NoError(t, f.approve())
	require.NoError(t, f.schedule(f.now.Add(time.Hour)))
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{facebook("Also on Facebook")}}))
	require.Equal(t, "in_review", f.post.Status)
	require.False(t, f.post.ScheduledAt.Valid)

	f = newPost(t, "insight")
	require.NoError(t, f.edit(content.Edit{Status: "idea"}))
	require.Equal(t, "idea", f.post.Status)
	require.NoError(t, f.edit(content.Edit{Title: "Still an idea"}))
	require.Equal(t, "idea", f.post.Status)
}

func TestApproveChecksEveryEnabledChannel(t *testing.T) {
	ig := db.SavePostVersionParams{Channel: "instagram", Enabled: true,
		Text: textOf(strings.Repeat("calm ", 441) + strings.Repeat("#art ", 31))}
	li := db.SavePostVersionParams{Channel: "linkedin", Enabled: true, Text: textOf(strings.Repeat("x", 3001))}
	site := website("checked-site")
	site.Excerpt = localized("", "မြန်မာ")
	off := db.SavePostVersionParams{Channel: "facebook", Enabled: false}
	f := newPost(t, "insight", site, ig, li, off)
	require.NoError(t, f.submit())

	e := requireCode(t, apperr.PublishRequirementsUnmet, f.approve())
	require.ElementsMatch(t, []apperr.FieldError{
		{Field: "/versions/website/excerpt/en", Message: "is required"},
		{Field: "/versions/instagram/imageIds", Message: "needs at least one image"},
		{Field: "/versions/instagram/caption", Message: "must be at most 2,200 characters"},
		{Field: "/versions/instagram/caption", Message: "must have at most 30 hashtags"},
		{Field: "/versions/linkedin/text", Message: "must be at most 3,000 characters"},
	}, e.Fields)
	require.Equal(t, "in_review", f.post.Status)

	ig.Text, ig.ImageIds = textOf("Calm, in colour. #arttherapy"), randomIDs(2)
	li.Text = textOf("A new article on finding calm.")
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{website("checked-site"), ig, li}}))
	require.NoError(t, f.approve())
}

func TestApproveNeedsAnEnabledChannel(t *testing.T) {
	f := newPost(t, "insight")
	require.NoError(t, f.submit())
	e := requireCode(t, apperr.PublishRequirementsUnmet, f.approve())
	require.Equal(t, []apperr.FieldError{{Field: "/versions", Message: "needs at least one enabled channel"}}, e.Fields)
}

func TestTrueStoryNeedsConsentBeforeApproval(t *testing.T) {
	f := newPost(t, "true_story", facebook("A story shared with permission."))
	require.NoError(t, f.submit())
	e := requireCode(t, apperr.PublishRequirementsUnmet, f.approve())
	require.Equal(t, []apperr.FieldError{{Field: "/consent/confirmed", Message: "must be confirmed for a True Story"}}, e.Fields)

	require.NoError(t, f.edit(content.Edit{Consent: &content.Consent{Confirmed: true, Note: "Signed form in Drive"}}))
	require.True(t, f.post.ConsentConfirmedAt.Valid)
	require.Equal(t, f.by, f.post.ConsentConfirmedBy)
	require.NoError(t, f.approve())
}
