package meta_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/meta"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
)

const (
	userToken = "user-token-long-lived"
	pageToken = "page-token-secret"
)

// call is one request the fake Graph API received.
type call struct {
	Method, Path string
	Params       url.Values
}

// graph is a fake Graph API: answers maps "METHOD path" (without the
// version) to a status and a JSON body; anything else is a 404.
type graph struct {
	mu      sync.Mutex
	answers map[string][]answer
	calls   []call
}

type answer struct {
	status int
	body   string
}

// on sets the answers for a route, in turn; the last one repeats.
func (g *graph) on(route string, status int, body ...string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.answers[route] = nil
	for _, b := range body {
		g.answers[route] = append(g.answers[route], answer{status, b})
	}
}

func (g *graph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	route := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/v24.0/")
	g.mu.Lock()
	g.calls = append(g.calls, call{r.Method, strings.TrimPrefix(r.URL.Path, "/v24.0/"), r.Form})
	queue := g.answers[route]
	a := answer{http.StatusNotFound, `{"error": {"message": "unknown route", "code": 803}}`}
	if len(queue) > 0 {
		a = queue[0]
		if len(queue) > 1 {
			g.answers[route] = queue[1:]
		}
	}
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.body))
}

func (g *graph) find(path string) []call {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []call
	for _, c := range g.calls {
		if c.Path == path {
			out = append(out, c)
		}
	}
	return out
}

func newConnector(t *testing.T) (*meta.Connector, *graph) {
	t.Helper()
	_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM connections")
	require.NoError(t, err)
	g := &graph{answers: map[string][]answer{}}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	tokens, err := auth.NewTOTP([]byte("0123456789abcdef0123456789abcdef"), func() time.Time { return now })
	require.NoError(t, err)
	return &meta.Connector{
		Pool: pgtest.Pool(t), Tokens: tokens, AppID: "app-1", AppSecret: "app-secret", Version: "v24.0",
		GraphURL: srv.URL, RedirectURL: "https://vetmimi.example/admin/settings/connections",
		SigningSecret: []byte("test signing secret, 32 bytes ok"), MediaPublicURL: "https://media.vetmimi.example",
		PollEvery: time.Millisecond, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}, g
}

func newUser(t *testing.T) pgtype.UUID {
	t.Helper()
	id, err := db.New(pgtest.Pool(t)).CreateUser(ctx, db.CreateUserParams{
		Email: strings.ToLower(rand.Text()) + "@example.com", DisplayName: "Daw Mi", PasswordHash: "x",
		Roles: []string{"site_admin"}, TotpSecretEnc: []byte("sealed"),
	})
	require.NoError(t, err)
	return id
}

func requireCode(t *testing.T, code apperr.Code, err error) {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, e.Detail)
}

func stateOf(t *testing.T, c *meta.Connector, user pgtype.UUID, at time.Time) string {
	t.Helper()
	link, err := c.AuthorizeURL(user, at)
	require.NoError(t, err)
	u, err := url.Parse(link)
	require.NoError(t, err)
	return u.Query().Get("state")
}

// login answers the code exchange and the long-lived token exchange.
func login(g *graph) {
	g.on("GET oauth/access_token", http.StatusOK, `{"access_token": "short", "expires_in": 3600}`,
		`{"access_token": "`+userToken+`", "expires_in": 5184000}`)
	g.on("GET debug_token", http.StatusOK, `{"data": {"expires_at": 0, "data_access_expires_at": 1799000000}}`)
}

const onePage = `{"data": [{"id": "page-1", "name": "VetMiMi", "access_token": "` + pageToken + `",
	"instagram_business_account": {"id": "ig-1", "username": "vetmimi"}}]}`

// Daw Mi signs in to Facebook from the Connections page; with one Page, it
// is connected at once with its Instagram account, its token sealed.
func TestConnectWithOnePage(t *testing.T) {
	c, g := newConnector(t)
	user := newUser(t)

	link, err := c.AuthorizeURL(user, now)
	require.NoError(t, err)
	u, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "/v24.0/dialog/oauth", u.Path)
	require.Equal(t, "https://vetmimi.example/admin/settings/connections", u.Query().Get("redirect_uri"))
	require.Contains(t, u.Query().Get("scope"), "instagram_content_publish")
	st := u.Query().Get("state")
	c.ConfigID = "config-1" // Facebook Login for Business names the permissions in its configuration
	withConfig, err := c.AuthorizeURL(user, now)
	require.NoError(t, err)
	require.Contains(t, withConfig, "config_id=config-1")
	require.NotContains(t, withConfig, "scope=")

	login(g)
	g.on("GET me/accounts", http.StatusOK, onePage)
	_, err = c.Finish(ctx, newUser(t), "code-1", st, now)
	requireCode(t, apperr.ActionNotAllowed, err) // another administrator's attempt
	_, err = c.Finish(ctx, user, "code-1", st, now.Add(11*time.Minute))
	requireCode(t, apperr.ActionNotAllowed, err) // expired
	_, err = c.Finish(ctx, user, "code-1", st+"x", now)
	requireCode(t, apperr.ActionNotAllowed, err) // tampered
	require.Empty(t, g.calls, "a bad state reaches no Graph call")

	conn, err := c.Finish(ctx, user, "code-1", st, now)
	require.NoError(t, err)
	require.Equal(t, "connected", conn.Status)
	require.Equal(t, "VetMiMi", conn.PageName)
	require.Equal(t, "vetmimi", conn.InstagramUsername)
	require.Equal(t, time.Unix(1799000000, 0).UTC(), conn.ExpiresAt.Time.UTC())
	exchange := g.find("oauth/access_token")[0].Params
	require.Equal(t, "code-1", exchange.Get("code"))
	require.Equal(t, c.RedirectURL, exchange.Get("redirect_uri"))
	accounts := g.find("me/accounts")[0].Params
	require.Equal(t, userToken, accounts.Get("access_token"))
	require.NotEmpty(t, accounts.Get("appsecret_proof"))

	row, err := db.New(pgtest.Pool(t)).GetConnection(ctx, "meta")
	require.NoError(t, err)
	require.NotContains(t, string(row.Token), pageToken)
	opened, err := c.Tokens.Open(row.Token)
	require.NoError(t, err)
	require.Equal(t, pageToken, string(opened))

	require.NoError(t, c.Disconnect(ctx))
	conn, err = c.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "not_connected", conn.Status)
	_, err = c.PublishFacebook(ctx, db.PostVersion{Text: pgtype.Text{String: "Hi", Valid: true}})
	require.ErrorIs(t, err, content.ErrNotConnected)
}

// With several Pages, Daw Mi chooses one from the list.
func TestChoosePageAmongSeveral(t *testing.T) {
	c, g := newConnector(t)
	user := newUser(t)
	_, err := c.ChoosePage(ctx, user, "page-2", now)
	requireCode(t, apperr.ActionNotAllowed, err)

	login(g)
	g.on("GET me/accounts", http.StatusOK, `{"data": [
		{"id": "page-1", "name": "Someone else's", "access_token": "other"},
		{"id": "page-2", "name": "VetMiMi", "access_token": "`+pageToken+`"}]}`)
	conn, err := c.Finish(ctx, user, "code-1", stateOf(t, c, user, now), now)
	require.NoError(t, err)
	require.Equal(t, "choosing_page", conn.Status)
	require.Len(t, conn.Pages, 2)
	require.Equal(t, "VetMiMi", conn.Pages[1].Name)

	_, err = c.ChoosePage(ctx, user, "page-9", now)
	requireCode(t, apperr.NotFound, err)
	conn, err = c.ChoosePage(ctx, user, "page-2", now)
	require.NoError(t, err)
	require.Equal(t, "connected", conn.Status)
	require.Equal(t, "page-2", conn.PageID)
	require.Empty(t, conn.InstagramID, "no Instagram account linked")
	_, err = c.PublishInstagram(ctx, db.PostVersion{})
	require.ErrorIs(t, err, content.ErrNotConnected)
}

// Facebook refusing the code is shown to Daw Mi in its own words.
func TestFacebookRefusesTheCode(t *testing.T) {
	c, g := newConnector(t)
	user := newUser(t)
	g.on("GET oauth/access_token", http.StatusBadRequest,
		`{"error": {"message": "This authorization code has expired.", "code": 100}}`)
	_, err := c.Finish(ctx, user, "old", stateOf(t, c, user, now), now)
	requireCode(t, apperr.ActionNotAllowed, err)
	require.Contains(t, err.Error(), "This authorization code has expired.")
}

func connected(t *testing.T) (*meta.Connector, *graph) {
	t.Helper()
	c, g := newConnector(t)
	user := newUser(t)
	login(g)
	g.on("GET me/accounts", http.StatusOK, onePage)
	_, err := c.Finish(ctx, user, "code-1", stateOf(t, c, user, now), now)
	require.NoError(t, err)
	return c, g
}

func newMedia(t *testing.T) pgtype.UUID {
	t.Helper()
	m, err := db.New(pgtest.Pool(t)).CreateMedia(ctx, db.CreateMediaParams{Width: 1600, Height: 1200,
		Widths: []int32{1600, 800, 400}, ByteSize: 1000, Now: now})
	require.NoError(t, err)
	return m.ID
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

// Facebook: a text with its link as a link post; with images, each
// uploaded unpublished and attached to one post with the link in the text.
func TestPublishToFacebook(t *testing.T) {
	c, g := connected(t)
	g.on("POST page-1/feed", http.StatusOK, `{"id": "page-1_100"}`, `{"id": "page-1_101"}`)
	g.on("GET page-1_100", http.StatusOK, `{"permalink_url": "https://www.facebook.com/vetmimi/posts/100"}`)
	g.on("POST page-1/photos", http.StatusOK, `{"id": "photo-1"}`, `{"id": "photo-2"}`)

	posted, err := c.PublishFacebook(ctx, db.PostVersion{Text: text("New article."),
		LinkUrl: text("https://vetmimi.example/articles/calm")})
	require.NoError(t, err)
	require.Equal(t, content.Posted{ExternalID: "page-1_100", Permalink: "https://www.facebook.com/vetmimi/posts/100"}, posted)
	feed := g.find("page-1/feed")[0].Params
	require.Equal(t, "New article.", feed.Get("message"))
	require.Equal(t, "https://vetmimi.example/articles/calm", feed.Get("link"))
	require.Equal(t, pageToken, feed.Get("access_token"))

	images := []pgtype.UUID{newMedia(t), newMedia(t)}
	posted, err = c.PublishFacebook(ctx, db.PostVersion{Text: text("Two paintings."),
		LinkUrl: text("https://vetmimi.example"), ImageIds: images})
	require.NoError(t, err)
	require.Equal(t, "page-1_101", posted.ExternalID)
	require.Empty(t, posted.Permalink, "the post is out even if its link cannot be read")
	photos := g.find("page-1/photos")
	require.Len(t, photos, 2)
	require.Equal(t, "https://media.vetmimi.example/"+images[0].String()+"/1600.jpg", photos[0].Params.Get("url"))
	require.Equal(t, "false", photos[0].Params.Get("published"))
	feed = g.find("page-1/feed")[1].Params
	require.Equal(t, "Two paintings.\n\nhttps://vetmimi.example", feed.Get("message"))
	require.Empty(t, feed.Get("link"))
	require.JSONEq(t, `{"media_fbid": "photo-2"}`, feed.Get("attached_media[1]"))
}

// Instagram: a carousel of containers, polled until Instagram has
// processed it, then published.
func TestPublishToInstagram(t *testing.T) {
	c, g := connected(t)
	g.on("POST ig-1/media", http.StatusOK, `{"id": "child-1"}`, `{"id": "child-2"}`, `{"id": "carousel-1"}`)
	g.on("GET carousel-1", http.StatusOK, `{"status_code": "IN_PROGRESS"}`, `{"status_code": "FINISHED"}`)
	g.on("POST ig-1/media_publish", http.StatusOK, `{"id": "ig-media-1"}`)
	g.on("GET ig-media-1", http.StatusOK, `{"permalink": "https://www.instagram.com/p/abc/"}`)

	images := []pgtype.UUID{newMedia(t), newMedia(t)}
	posted, err := c.PublishInstagram(ctx, db.PostVersion{Text: text("Colour #arttherapy"), ImageIds: images})
	require.NoError(t, err)
	require.Equal(t, content.Posted{ExternalID: "ig-media-1", Permalink: "https://www.instagram.com/p/abc/"}, posted)
	containers := g.find("ig-1/media")
	require.Equal(t, "true", containers[0].Params.Get("is_carousel_item"))
	require.Equal(t, "CAROUSEL", containers[2].Params.Get("media_type"))
	require.Equal(t, "child-1,child-2", containers[2].Params.Get("children"))
	require.Equal(t, "Colour #arttherapy", containers[2].Params.Get("caption"))
	require.Len(t, g.find("carousel-1"), 2)
	require.Equal(t, "carousel-1", g.find("ig-1/media_publish")[0].Params.Get("creation_id"))
}

func reason(t *testing.T, err error) *content.ChannelError {
	t.Helper()
	var ce *content.ChannelError
	require.ErrorAs(t, err, &ce)
	return ce
}

// A failure before the post is sent is tried again; a lost answer to the
// call that posts may hide a post that went out, so it is never sent again
// by itself; a dead token asks Daw Mi to reconnect.
func TestPublishFailures(t *testing.T) {
	c, g := connected(t)
	image := []pgtype.UUID{newMedia(t)}

	g.on("POST page-1/photos", http.StatusServiceUnavailable, `{}`)
	ce := reason(t, errOf(c.PublishFacebook(ctx, db.PostVersion{Text: text("x"), ImageIds: image})))
	require.Equal(t, &content.ChannelError{Reason: "connection_failed", Transient: true}, ce)

	g.on("POST page-1/feed", http.StatusInternalServerError, `{"error": {"message": "Unknown error", "code": 1}}`)
	ce = reason(t, errOf(c.PublishFacebook(ctx, db.PostVersion{Text: text("x")})))
	require.Equal(t, &content.ChannelError{Reason: "unknown_outcome"}, ce)

	g.on("POST ig-1/media", http.StatusOK, `{"id": "c-1"}`)
	g.on("GET c-1", http.StatusOK, `{"status_code": "ERROR"}`)
	ce = reason(t, errOf(c.PublishInstagram(ctx, db.PostVersion{Text: text("x"), ImageIds: image})))
	require.Equal(t, &content.ChannelError{Reason: "rejected"}, ce)

	g.on("POST page-1/feed", http.StatusBadRequest, `{"error": {"message": "Session has expired", "code": 190}}`)
	ce = reason(t, errOf(c.PublishFacebook(ctx, db.PostVersion{Text: text("x")})))
	require.Equal(t, &content.ChannelError{Reason: "reconnect_required"}, ce)
	conn, err := c.Status(ctx)
	require.NoError(t, err)
	require.Equal(t, "reconnect_required", conn.LastError)
}

func errOf(_ content.Posted, err error) error { return err }

// The scheduler's channel task posts through the connector: Facebook
// published with its id and address once connected.
func TestSchedulerPublishesThroughTheConnector(t *testing.T) {
	c, g := connected(t)
	g.on("POST page-1/feed", http.StatusOK, `{"id": "page-1_7"}`)
	g.on("GET page-1_7", http.StatusOK, `{"permalink_url": "https://www.facebook.com/7"}`)
	pool := pgtest.Pool(t)
	author := newUser(t)
	p, err := content.CreatePost(ctx, pool, content.Edit{Title: "Open day", Kind: "announcement",
		Versions: []db.SavePostVersionParams{{Channel: "facebook", Enabled: true, Text: text("Open day.")}}}, author, now)
	require.NoError(t, err)
	for _, step := range []func() (content.Post, error){
		func() (content.Post, error) { return content.Submit(ctx, pool, p.ID, p.Version, now) },
		func() (content.Post, error) { return content.Approve(ctx, pool, p.ID, p.Version, author, now) },
		func() (content.Post, error) { return content.PublishNow(ctx, pool, p.ID, p.Version, now) },
	} {
		p, err = step()
		require.NoError(t, err)
	}
	tasks := &content.Tasks{Pool: pool, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
		Publishers: map[string]content.Publisher{"facebook": c.PublishFacebook}}
	body, err := json.Marshal(content.PublishTasks(p)[0].Payload)
	require.NoError(t, err)
	require.NoError(t, tasks.PublishChannel(ctx, body))
	p, err = content.GetPost(ctx, db.New(pool), p.ID)
	require.NoError(t, err)
	require.Equal(t, "published", p.Status)
	require.Equal(t, "https://www.facebook.com/7", p.Publications[0].Permalink.String)
}
