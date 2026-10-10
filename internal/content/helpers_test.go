package content_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
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

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var ctx = context.Background()

func requireCode(t *testing.T, code apperr.Code, err error) *apperr.Error {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, e.Detail)
	return e
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

func localized(en, my string) json.RawMessage {
	m := map[string]string{"en": en}
	if my != "" {
		m["my"] = my
	}
	b, _ := json.Marshal(m)
	return b
}

// website is a complete website version under slug.
func website(slug string) db.SavePostVersionParams {
	return db.SavePostVersionParams{Channel: "website", Enabled: true, Slug: textOf(slug),
		Title: localized("Finding calm", "ငြိမ်သက်မှု"), Excerpt: localized("A short read.", ""),
		Body: localized("# Calm\n\nBreathe.", "# ငြိမ်\n\nအသက်ရှူပါ။")}
}

func facebook(text string) db.SavePostVersionParams {
	return db.SavePostVersionParams{Channel: "facebook", Enabled: true, Text: textOf(text)}
}

func textOf(s string) pgtype.Text { return pgtype.Text{String: s, Valid: true} }

func randomIDs(n int) []pgtype.UUID {
	ids := make([]pgtype.UUID, n)
	for i := range ids {
		ids[i] = pgtype.UUID{Valid: true}
		_, _ = rand.Read(ids[i].Bytes[:])
	}
	return ids
}

// newMedia inserts a media library row without files.
func newMedia(t *testing.T) pgtype.UUID {
	t.Helper()
	m, err := db.New(pgtest.Pool(t)).CreateMedia(ctx, db.CreateMediaParams{Width: 1600, Height: 900,
		Widths: []int32{1600, 800, 400}, ByteSize: 1000, Alt: localized("A blue painting", "အပြာရောင်ပန်းချီ"),
		Now: time.Now()})
	require.NoError(t, err)
	return m.ID
}

// fixture is a post moved along the workflow by the test's author.
type fixture struct {
	t    *testing.T
	by   pgtype.UUID
	now  time.Time
	post content.Post
}

func newPost(t *testing.T, kind string, versions ...db.SavePostVersionParams) *fixture {
	t.Helper()
	f := &fixture{t: t, by: newUser(t), now: time.Date(2026, 10, 7, 9, 0, 0, 0, time.UTC)}
	p, err := content.CreatePost(ctx, pgtest.Pool(t), content.Edit{Title: "Finding calm", Kind: kind,
		Versions: versions}, f.by, f.now)
	require.NoError(t, err)
	require.Equal(t, "draft", p.Status)
	f.post = p
	return f
}

// step runs act as of the post's current version and keeps the result.
func (f *fixture) step(act func(id pgtype.UUID, version int32) (content.Post, error)) error {
	f.t.Helper()
	p, err := act(f.post.ID, f.post.Version)
	if err == nil {
		f.post = p
	}
	return err
}

func (f *fixture) submit() error {
	return f.step(func(id pgtype.UUID, v int32) (content.Post, error) {
		return content.Submit(ctx, pgtest.Pool(f.t), id, v, f.now)
	})
}

func (f *fixture) approve() error {
	return f.step(func(id pgtype.UUID, v int32) (content.Post, error) {
		return content.Approve(ctx, pgtest.Pool(f.t), id, v, f.by, f.now)
	})
}

func (f *fixture) edit(e content.Edit) error {
	return f.step(func(id pgtype.UUID, v int32) (content.Post, error) {
		return content.UpdatePost(ctx, pgtest.Pool(f.t), id, v, e, f.by, f.now)
	})
}

func (f *fixture) publish() error {
	return f.step(func(id pgtype.UUID, v int32) (content.Post, error) {
		return content.PublishNow(ctx, pgtest.Pool(f.t), id, v, f.now)
	})
}

func (f *fixture) schedule(at time.Time) error {
	return f.step(func(id pgtype.UUID, v int32) (content.Post, error) {
		return content.Schedule(ctx, pgtest.Pool(f.t), id, v, at, f.now)
	})
}

func (f *fixture) published() *fixture {
	f.t.Helper()
	require.NoError(f.t, f.submit())
	require.NoError(f.t, f.approve())
	require.NoError(f.t, f.publish())
	return f
}

func publication(p content.Post, channel string) db.PostPublication {
	for _, pub := range p.Publications {
		if pub.Channel == channel {
			return pub
		}
	}
	return db.PostPublication{}
}
