package content_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func TestCreateKeepsVersionsAndListShowsEnabledChannels(t *testing.T) {
	off := facebook("Later")
	off.Enabled = false
	f := newPost(t, "insight", website("create-and-list"), off)
	require.Len(t, f.post.Versions, 2)

	got, err := content.GetPost(ctx, db.New(pgtest.Pool(t)), f.post.ID)
	require.NoError(t, err)
	require.Equal(t, f.post, got)

	page, err := content.ListPosts(ctx, db.New(pgtest.Pool(t)), content.PostFilter{Status: "draft", Search: "calm"})
	require.NoError(t, err)
	var channels []string
	for _, r := range page.Items {
		if r.ID == f.post.ID {
			channels = r.Channels
		}
	}
	require.Equal(t, []string{"website"}, channels)
}

func TestSlugIsUniqueAmongArticles(t *testing.T) {
	newPost(t, "insight", website("one-slug"))
	_, err := content.CreatePost(ctx, pgtest.Pool(t), content.Edit{Title: "Again", Kind: "insight",
		Versions: []db.SavePostVersionParams{website("one-slug")}}, newUser(t), time.Now())
	requireCode(t, apperr.SlugTaken, err)
}

func TestDeleteOnlyIdeasAndDrafts(t *testing.T) {
	f := newPost(t, "insight")
	require.NoError(t, content.DeletePost(ctx, db.New(pgtest.Pool(t)), f.post.ID))
	requireCode(t, apperr.NotFound, content.DeletePost(ctx, db.New(pgtest.Pool(t)), f.post.ID))

	f = newPost(t, "insight")
	require.NoError(t, f.submit())
	requireCode(t, apperr.InvalidTransition, content.DeletePost(ctx, db.New(pgtest.Pool(t)), f.post.ID))
}
