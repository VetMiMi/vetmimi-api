package linkedin_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/linkedin"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/secretbox"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var (
	ctx = context.Background()
	now = time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)
)

const memberToken = "member-token-secret"

// request is one request the fake LinkedIn received.
type request struct {
	Method, Path string
	Header       http.Header
	Body         []byte
}

// fake is LinkedIn's OAuth, REST and upload endpoints, and the media
// bucket: answers maps "METHOD path" to a reply.
type fake struct {
	mu       sync.Mutex
	answers  map[string]answer
	requests []request
}

type answer struct {
	status int
	body   string
	id     string
}

func (f *fake) on(route string, status int, body, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[route] = answer{status, body, id}
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, request{r.Method, r.URL.Path, r.Header.Clone(), body})
	a, ok := f.answers[r.Method+" "+r.URL.Path]
	f.mu.Unlock()
	if !ok {
		a = answer{status: http.StatusNotFound, body: `{"status": 404, "serviceErrorCode": 0}`}
	}
	if a.id != "" {
		w.Header().Set("X-Restli-Id", a.id)
	}
	w.WriteHeader(a.status)
	_, _ = w.Write([]byte(a.body))
}

func (f *fake) find(path string) []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []request
	for _, r := range f.requests {
		if r.Path == path {
			out = append(out, r)
		}
	}
	return out
}

func newConnector(t *testing.T) (*linkedin.Connector, *fake) {
	t.Helper()
	_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM connections")
	require.NoError(t, err)
	f := &fake{answers: map[string]answer{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	tokens, err := secretbox.New([]byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	return &linkedin.Connector{
		Pool: pgtest.Pool(t), Tokens: tokens, ClientID: "client-1", ClientSecret: "client-secret",
		Version: "202609", AuthURL: srv.URL, APIURL: srv.URL,
		RedirectURL:   "https://vetmimi.example/admin/settings/connections/linkedin",
		SigningSecret: []byte("test signing secret, 32 bytes ok"), MediaPublicURL: srv.URL + "/media",
		Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return now },
	}, f
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

func stateOf(t *testing.T, c *linkedin.Connector, user pgtype.UUID) string {
	t.Helper()
	link, err := c.AuthorizeURL(user, now)
	require.NoError(t, err)
	u, err := url.Parse(link)
	require.NoError(t, err)
	return u.Query().Get("state")
}

// signIn answers the code exchange with a 60-day token and the member's profile.
func signIn(f *fake, scope string) {
	f.on("POST /oauth/v2/accessToken", http.StatusOK,
		`{"access_token": "`+memberToken+`", "expires_in": 5184000, "scope": "`+scope+`"}`, "")
	f.on("GET /v2/userinfo", http.StatusOK, `{"sub": "abc123", "name": "Daw Mi"}`, "")
}

// The status warns a week before the token expires and asks for a reconnect once it has.
func TestConnect(t *testing.T) {
	c, f := newConnector(t)
	user := newUser(t)

	link, err := c.AuthorizeURL(user, now)
	require.NoError(t, err)
	u, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "/oauth/v2/authorization", u.Path)
	require.Equal(t, "openid profile w_member_social", u.Query().Get("scope"))
	require.Equal(t, c.RedirectURL, u.Query().Get("redirect_uri"))
	st := u.Query().Get("state")

	signIn(f, "email,openid,profile,w_member_social")
	_, err = c.Finish(ctx, newUser(t), "code-1", st, now)
	requireCode(t, apperr.ActionNotAllowed, err) // another administrator's attempt
	_, err = c.Finish(ctx, user, "code-1", st, now.Add(11*time.Minute))
	requireCode(t, apperr.ActionNotAllowed, err) // expired
	require.Empty(t, f.requests, "a bad state reaches no LinkedIn call")

	conn, err := c.Finish(ctx, user, "code-1", st, now)
	require.NoError(t, err)
	require.Equal(t, linkedin.Connection{Status: "connected", MemberName: "Daw Mi",
		ExpiresAt: conn.ExpiresAt, ConnectedAt: conn.ConnectedAt}, conn)
	require.Equal(t, now.Add(60*24*time.Hour), conn.ExpiresAt.Time.UTC())
	exchange, err := url.ParseQuery(string(f.find("/oauth/v2/accessToken")[0].Body))
	require.NoError(t, err)
	require.Equal(t, "code-1", exchange.Get("code"))
	require.Equal(t, c.RedirectURL, exchange.Get("redirect_uri"))
	require.Equal(t, "Bearer "+memberToken, f.find("/v2/userinfo")[0].Header.Get("Authorization"))

	row, err := db.New(pgtest.Pool(t)).GetConnection(ctx, "linkedin")
	require.NoError(t, err)
	require.Equal(t, "abc123", row.AccountID.String)
	require.NotContains(t, string(row.Token), memberToken)

	for at, want := range map[time.Time]string{
		now.Add(52 * 24 * time.Hour): "connected",
		now.Add(54 * 24 * time.Hour): "expiring_soon",
		now.Add(60 * 24 * time.Hour): "reconnect_required",
	} {
		conn, err = c.Status(ctx, at)
		require.NoError(t, err)
		require.Equal(t, want, conn.Status, at)
	}

	require.NoError(t, c.Disconnect(ctx))
	conn, err = c.Status(ctx, now)
	require.NoError(t, err)
	require.Equal(t, "not_connected", conn.Status)
	_, err = c.Publish(ctx, db.PostVersion{Text: text("Hi")})
	require.ErrorIs(t, err, content.ErrNotConnected)
}

// A sign-in without sharing, or a refused code, does not connect.
func TestConnectRefused(t *testing.T) {
	c, f := newConnector(t)
	user := newUser(t)
	signIn(f, "openid,profile")
	_, err := c.Finish(ctx, user, "code-1", stateOf(t, c, user), now)
	requireCode(t, apperr.ActionNotAllowed, err)

	f.on("POST /oauth/v2/accessToken", http.StatusBadRequest, `{"error": "invalid_request"}`, "")
	_, err = c.Finish(ctx, user, "code-1", stateOf(t, c, user), now)
	requireCode(t, apperr.ActionNotAllowed, err)
	conn, err := c.Status(ctx, now)
	require.NoError(t, err)
	require.Equal(t, "not_connected", conn.Status)

	c.ClientID = ""
	_, err = c.AuthorizeURL(user, now)
	requireCode(t, apperr.Unavailable, err)
}

func connected(t *testing.T) (*linkedin.Connector, *fake) {
	t.Helper()
	c, f := newConnector(t)
	user := newUser(t)
	signIn(f, "openid,profile,w_member_social")
	_, err := c.Finish(ctx, user, "code-1", stateOf(t, c, user), now)
	require.NoError(t, err)
	return c, f
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func posted(t *testing.T, f *fake, i int) map[string]any {
	t.Helper()
	req := f.find("/rest/posts")[i]
	require.Equal(t, "202609", req.Header.Get("LinkedIn-Version"))
	require.Equal(t, "2.0.0", req.Header.Get("X-Restli-Protocol-Version"))
	require.Equal(t, "Bearer "+memberToken, req.Header.Get("Authorization"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(req.Body, &body))
	return body
}

// Text alone, text with an article link, and text with one image.
func TestPublish(t *testing.T) {
	c, f := connected(t)
	f.on("POST /rest/posts", http.StatusCreated, "", "urn:li:share:7001")

	out, err := c.Publish(ctx, db.PostVersion{Text: text("Colour (and calm) #art_therapy")})
	require.NoError(t, err)
	require.Equal(t, content.Posted{ExternalID: "urn:li:share:7001",
		Permalink: "https://www.linkedin.com/feed/update/urn:li:share:7001/"}, out)
	body := posted(t, f, 0)
	require.Equal(t, "urn:li:person:abc123", body["author"])
	require.Equal(t, "PUBLIC", body["visibility"])
	require.Equal(t, "MAIN_FEED", body["distribution"].(map[string]any)["feedDistribution"])
	require.Equal(t, `Colour \(and calm\) #art_therapy`, body["commentary"], "reserved characters escaped, hashtags kept")
	require.NotContains(t, body, "content")

	p, err := content.CreatePost(ctx, pgtest.Pool(t), content.Edit{Title: "Finding calm", Kind: "insight"},
		newUser(t), now)
	require.NoError(t, err)
	_, err = c.Publish(ctx, db.PostVersion{PostID: p.ID, Text: text("New article."),
		LinkUrl: text("https://vetmimi.example/articles/calm")})
	require.NoError(t, err)
	require.Equal(t, map[string]any{"article": map[string]any{
		"source": "https://vetmimi.example/articles/calm", "title": "Finding calm"}}, posted(t, f, 1)["content"])

	image, err := db.New(pgtest.Pool(t)).CreateMedia(ctx, db.CreateMediaParams{Width: 1600, Height: 1200,
		Widths: []int32{1600, 800}, ByteSize: 1000, Alt: json.RawMessage(`{"en": "A painting"}`), Now: now})
	require.NoError(t, err)
	f.on("GET /media/"+image.ID.String()+"/1600.jpg", http.StatusOK, "jpeg-bytes", "")
	f.on("POST /rest/images", http.StatusOK, `{"value": {"uploadUrl": "`+c.APIURL+`/upload/1",
		"image": "urn:li:image:C4E"}}`, "")
	f.on("PUT /upload/1", http.StatusCreated, "", "")
	_, err = c.Publish(ctx, db.PostVersion{Text: text("A painting."), LinkUrl: text("https://vetmimi.example"),
		ImageIds: []pgtype.UUID{image.ID}})
	require.NoError(t, err)
	var start map[string]any
	require.NoError(t, json.Unmarshal(f.find("/rest/images")[0].Body, &start))
	require.Equal(t, map[string]any{"owner": "urn:li:person:abc123"}, start["initializeUploadRequest"])
	upload := f.find("/upload/1")[0]
	require.True(t, bytes.Equal([]byte("jpeg-bytes"), upload.Body))
	require.Equal(t, "Bearer "+memberToken, upload.Header.Get("Authorization"))
	body = posted(t, f, 2)
	require.Equal(t, map[string]any{"media": map[string]any{"id": "urn:li:image:C4E", "altText": "A painting"}},
		body["content"])
	require.Equal(t, "A painting.\n\nhttps://vetmimi.example", body["commentary"])
}

func reason(t *testing.T, err error) *content.ChannelError {
	t.Helper()
	var ce *content.ChannelError
	require.ErrorAs(t, err, &ce)
	return ce
}

func errOf(_ content.Posted, err error) error { return err }

// A refusal is final, a rate limit is retried, a lost answer is
// unknown_outcome, and a refused or expired token needs a reconnect.
func TestPublishFailures(t *testing.T) {
	c, f := connected(t)
	v := db.PostVersion{Text: text("x")}

	f.on("POST /rest/posts", http.StatusUnprocessableEntity, `{"status": 422, "serviceErrorCode": 65600}`, "")
	require.Equal(t, &content.ChannelError{Reason: "rejected"}, reason(t, errOf(c.Publish(ctx, v))))

	f.on("POST /rest/posts", http.StatusTooManyRequests, `{"status": 429}`, "")
	require.Equal(t, &content.ChannelError{Reason: "connection_failed", Transient: true},
		reason(t, errOf(c.Publish(ctx, v))))

	f.on("POST /rest/posts", http.StatusInternalServerError, `{"status": 500}`, "")
	require.Equal(t, &content.ChannelError{Reason: "unknown_outcome"}, reason(t, errOf(c.Publish(ctx, v))))

	f.on("POST /rest/posts", http.StatusUnauthorized, `{"status": 401, "serviceErrorCode": 65601}`, "")
	require.Equal(t, &content.ChannelError{Reason: "reconnect_required"}, reason(t, errOf(c.Publish(ctx, v))))
	conn, err := c.Status(ctx, now)
	require.NoError(t, err)
	require.Equal(t, "reconnect_required", conn.Status)

	c, f = connected(t)
	c.Now = func() time.Time { return now.Add(61 * 24 * time.Hour) }
	require.Equal(t, &content.ChannelError{Reason: "reconnect_required"}, reason(t, errOf(c.Publish(ctx, v))))
	require.Empty(t, f.find("/rest/posts"))
}
