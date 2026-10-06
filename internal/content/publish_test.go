package content_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

var quiet = slog.New(slog.DiscardHandler)

// worker is the publishing worker with publish standing in for every
// channel's connector, and a clock the test moves.
func worker(t *testing.T, at *time.Time, publish content.Publisher) *content.Tasks {
	t.Helper()
	tasks := &content.Tasks{Pool: pgtest.Pool(t), Log: quiet, Now: func() time.Time { return *at }}
	if publish != nil {
		tasks.Publishers = map[string]content.Publisher{"facebook": publish, "instagram": publish, "linkedin": publish}
	}
	return tasks
}

func payload(t *testing.T, task platform.Task) []byte {
	t.Helper()
	b, err := json.Marshal(task.Payload)
	require.NoError(t, err)
	return b
}

func reload(t *testing.T, id pgtype.UUID) content.Post {
	t.Helper()
	p, err := content.GetPost(ctx, db.New(pgtest.Pool(t)), id)
	require.NoError(t, err)
	return p
}

// A scheduled post's task runs at its time and starts publishing: the
// website at once, each social channel as a task of its own. Run early, or
// after the schedule moved, it does nothing.
func TestScheduledPostPublishesAtItsTime(t *testing.T) {
	f := newPost(t, "insight", website("scheduled-article"), facebook("Out tomorrow."))
	require.NoError(t, f.submit())
	require.NoError(t, f.approve())
	at := f.now.Add(24 * time.Hour)
	require.NoError(t, f.schedule(at))

	tasks := content.ScheduledTask(f.post)
	require.Len(t, tasks, 1)
	require.Equal(t, content.TaskPublishScheduled, tasks[0].Type)
	require.True(t, at.Equal(tasks[0].ProcessAt))

	now := at.Add(-time.Second)
	w := worker(t, &now, nil)
	require.NoError(t, w.PublishScheduled(ctx, payload(t, tasks[0])))
	require.Equal(t, "scheduled", reload(t, f.post.ID).Status, "not yet due")

	now = at
	require.NoError(t, w.PublishScheduled(ctx, payload(t, tasks[0])))
	p := reload(t, f.post.ID)
	require.Equal(t, "publishing", p.Status)
	require.Equal(t, "published", publication(p, "website").Status)
	require.Equal(t, "pending", publication(p, "facebook").Status)

	channel := content.PublishTasks(p)
	require.Len(t, channel, 1)
	require.Equal(t, content.TaskPublishChannel, channel[0].Type)
	require.Equal(t, "publish:"+p.ID.String()+":facebook:0", channel[0].ID)
	revalidate := content.RevalidateTasks(p)
	require.Len(t, revalidate, 1)
	require.JSONEq(t, `{"slug": "scheduled-article"}`, string(payload(t, revalidate[0])))

	require.NoError(t, w.PublishScheduled(ctx, payload(t, tasks[0])))
	require.Equal(t, p.Version, reload(t, f.post.ID).Version, "a second run changes nothing")
}

// Until the connectors exist every social channel fails as not connected,
// once and for good, leaving copy & open; retry gives it another round.
func TestNotConnectedChannelFailsAndCanBeRetried(t *testing.T) {
	f := newPost(t, "announcement", facebook("Studio open day."), db.SavePostVersionParams{
		Channel: "linkedin", Enabled: true, Text: textOf("Studio open day.")}).published()
	now := f.now
	task := content.PublishTasks(f.post)[0]
	require.NoError(t, worker(t, &now, nil).PublishChannel(ctx, payload(t, task)))

	p := reload(t, f.post.ID)
	fb := publication(p, "facebook")
	require.Equal(t, "failed", fb.Status)
	require.Equal(t, "not_connected", fb.Error.String)
	require.EqualValues(t, 1, fb.Attempts)
	require.Equal(t, "publishing", p.Status)

	_, err := content.Retry(ctx, pgtest.Pool(t), p.ID, "linkedin", now)
	requireCode(t, apperr.ActionNotAllowed, err)
	p, err = content.Retry(ctx, pgtest.Pool(t), p.ID, "facebook", now)
	require.NoError(t, err)
	require.Equal(t, "pending", publication(p, "facebook").Status)
	require.Len(t, content.PublishTasks(p), 2)

	posted := func(context.Context, db.PostVersion) (content.Posted, error) {
		return content.Posted{ExternalID: "123_456", Permalink: "https://www.facebook.com/123_456"}, nil
	}
	w := worker(t, &now, posted)
	for _, task := range content.PublishTasks(p) {
		require.NoError(t, w.PublishChannel(ctx, payload(t, task)))
	}
	p = reload(t, f.post.ID)
	require.Equal(t, "published", p.Status)
	require.Equal(t, "https://www.facebook.com/123_456", publication(p, "facebook").Permalink.String)
	require.NoError(t, w.PublishChannel(ctx, payload(t, task)), "a duplicate task does nothing")
}

// A transient failure is tried three more times, the task failing each
// time so asynq backs off, and then the channel is failed.
func TestTransientFailureRetriesThreeTimes(t *testing.T) {
	f := newPost(t, "insight", facebook("Calm, in colour.")).published()
	now := f.now
	down := errors.New("connection reset")
	calls := 0
	w := worker(t, &now, func(context.Context, db.PostVersion) (content.Posted, error) {
		calls++
		return content.Posted{}, down
	})
	task := payload(t, content.PublishTasks(f.post)[0])
	for range 3 {
		require.ErrorIs(t, w.PublishChannel(ctx, task), down)
		require.Equal(t, "pending", publication(reload(t, f.post.ID), "facebook").Status)
	}
	require.NoError(t, w.PublishChannel(ctx, task))
	fb := publication(reload(t, f.post.ID), "facebook")
	require.Equal(t, "failed", fb.Status)
	require.Equal(t, "connection_failed", fb.Error.String)
	require.Equal(t, 4, calls)
}

// The sweep rebuilds what Redis lost: a scheduled post past its time, and
// a channel left waiting for ten minutes. A channel left running is never
// sent again, since it may have posted: the sweep fails it as
// unknown_outcome for Daw Mi to check, then retry or mark posted.
func TestSweepRebuildsLostTasks(t *testing.T) {
	scheduled := newPost(t, "insight", website("swept-article"))
	require.NoError(t, scheduled.submit())
	require.NoError(t, scheduled.approve())
	require.NoError(t, scheduled.schedule(scheduled.now.Add(time.Hour)))
	stuck := newPost(t, "insight", facebook("Lost task.")).published()

	q := db.New(pgtest.Pool(t))
	ids := func(now time.Time) []string {
		tasks, err := content.DueTasks(ctx, q, now)
		require.NoError(t, err)
		var out []string
		for _, task := range tasks {
			out = append(out, task.ID)
		}
		return out
	}
	scheduledID := content.ScheduledTask(scheduled.post)[0].ID
	channelID := content.PublishTasks(stuck.post)[0].ID
	require.NotContains(t, ids(stuck.now.Add(time.Minute)), channelID, "still with its own task")
	require.NotContains(t, ids(stuck.now.Add(time.Minute)), scheduledID, "not due")
	later := stuck.now.Add(time.Hour + 2*time.Minute)
	require.Subset(t, ids(later), []string{scheduledID, channelID})

	// A worker that died mid-attempt leaves the channel publishing.
	_, err := pgtest.Pool(t).Exec(ctx, `UPDATE post_publications SET status = 'publishing'
		WHERE post_id = $1 AND channel = 'facebook'`, stuck.post.ID)
	require.NoError(t, err)
	sent := 0
	send := func(context.Context, db.PostVersion) (content.Posted, error) {
		sent++
		return content.Posted{ExternalID: "1_2"}, nil
	}
	task := payload(t, content.PublishTasks(stuck.post)[0])
	now := stuck.now.Add(11 * time.Minute)
	require.NoError(t, worker(t, &now, send).PublishChannel(ctx, task))
	require.Zero(t, sent, "a running attempt is never sent again")
	require.NotContains(t, ids(now), channelID)

	require.NoError(t, worker(t, &now, send).Sweep(ctx, nil))
	fb := publication(reload(t, stuck.post.ID), "facebook")
	require.Equal(t, "failed", fb.Status)
	require.Equal(t, "unknown_outcome", fb.Error.String)
	require.NotContains(t, ids(now.Add(time.Hour)), channelID, "failed waits for Daw Mi")
	require.Zero(t, sent)
}

// The website channel asks the site to revalidate the articles list and the
// article; without the shared secret it skips the call.
func TestRevalidateCallsTheSite(t *testing.T) {
	var got *http.Request
	var body []byte
	status := http.StatusOK
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(status)
	}))
	t.Cleanup(site.Close)
	task := []byte(`{"slug": "finding-calm"}`)

	w := &content.Tasks{SiteURL: site.URL, Log: quiet}
	require.NoError(t, w.Revalidate(ctx, task))
	require.Nil(t, got, "no secret, no call")

	w.RevalidateSecret = "shared-secret"
	require.NoError(t, w.Revalidate(ctx, task))
	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/api/revalidate", got.URL.Path)
	require.Equal(t, "shared-secret", got.Header.Get("X-Revalidate-Secret"))
	require.JSONEq(t, `{"tags": ["articles", "article:finding-calm"]}`, string(body))

	status = http.StatusInternalServerError
	require.Error(t, w.Revalidate(ctx, task), "retried by asynq")
}

// A published article may be corrected: the edit goes back to review while
// visitors keep reading the article as published, and publishing again
// republishes the website only, keeping its first publication time; what
// went out on Facebook stays. A post that went out is never deleted.
func TestEditingAPublishedArticleRepublishesTheWebsite(t *testing.T) {
	f := newPost(t, "insight", website("corrected-article"), facebook("Read the new article.")).published()
	pool := pgtest.Pool(t)
	p, err := content.MarkPosted(ctx, pool, f.post.ID, "facebook", "", f.now)
	require.NoError(t, err)
	f.post = p
	require.Equal(t, "published", p.Status)
	firstPublished := publication(p, "website").PublishedAt

	requireCode(t, apperr.ActionNotAllowed, f.edit(content.Edit{Versions: []db.SavePostVersionParams{facebook("Edited")}}))
	corrected := website("corrected-article")
	corrected.Body = localized("# Calm\n\nBreathe slowly.", "")
	f.now = f.now.Add(time.Hour)
	require.NoError(t, f.edit(content.Edit{Versions: []db.SavePostVersionParams{corrected}}))
	require.Equal(t, "in_review", f.post.Status)
	article, err := content.GetArticle(ctx, db.New(pool), "corrected-article", "en")
	require.NoError(t, err, "still live while in review")
	require.Equal(t, "# Calm\n\nBreathe.", article.Body)
	p, err = content.RequestChanges(ctx, pool, f.post.ID, f.post.Version, "One more line.", f.now)
	require.NoError(t, err)
	require.Equal(t, "draft", p.Status)
	requireCode(t, apperr.InvalidTransition, content.DeletePost(ctx, db.New(pool), p.ID))
	f.post = p
	require.NoError(t, f.submit())
	_, err = content.GetArticle(ctx, db.New(pool), "corrected-article", "en")
	require.NoError(t, err)

	require.NoError(t, f.approve())
	require.NoError(t, f.publish())
	require.Equal(t, "published", f.post.Status)
	require.Equal(t, firstPublished, publication(f.post, "website").PublishedAt)
	require.Equal(t, "manual", publication(f.post, "facebook").Status)
	require.Empty(t, content.PublishTasks(f.post))
	article, err = content.GetArticle(ctx, db.New(pool), "corrected-article", "en")
	require.NoError(t, err)
	require.Equal(t, "# Calm\n\nBreathe slowly.", article.Body)
}
