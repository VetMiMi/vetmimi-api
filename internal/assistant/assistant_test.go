package assistant_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

var ctx = context.Background()

// fakeClaude answers every Messages API call with status and a text
// answer, and keeps the last request.
type fakeClaude struct {
	status     int
	answer     string
	stopReason string
	calls      int
	header     http.Header
	body       map[string]any
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.header = r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &f.body)
	if f.status != http.StatusOK {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"type": "error", "error": {"type": "overloaded_error", "message": "Overloaded"}}`))
		return
	}
	out, _ := json.Marshal(map[string]any{
		"content":     []map[string]string{{"type": "text", "text": f.answer}},
		"stop_reason": f.stopReason,
		"usage":       map[string]int{"input_tokens": 10, "output_tokens": 20},
	})
	_, _ = w.Write(out)
}

func newClient(t *testing.T, answer string) (*assistant.Client, *fakeClaude, *bytes.Buffer) {
	t.Helper()
	f := &fakeClaude{status: http.StatusOK, answer: answer, stopReason: "end_turn"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	return &assistant.Client{APIKey: "sk-test", Model: "claude-haiku-4-5-20251001", URL: srv.URL,
		Log: slog.New(slog.NewJSONHandler(logs, nil))}, f, logs
}

// post has a website version in English, and in Burmese when my is set.
func post(en, my string) content.Post {
	body, _ := json.Marshal(map[string]string{"en": en, "my": my})
	title, _ := json.Marshal(map[string]string{"en": "Finding calm", "my": my})
	return content.Post{Versions: []db.PostVersion{
		{Channel: "website", Title: title, Excerpt: json.RawMessage(`{}`), Body: body},
	}}
}

const article = "Painting slowly helps me notice my breath."

// The suggestions come back for each channel asked for, from an answer
// with prose around its JSON; hashtags are tidied. The request carries the
// key and version headers and the post's own text, and the log keeps
// neither the prompt nor the answer.
func TestSuggest(t *testing.T) {
	c, f, logs := newClient(t, "Here you go:\n```json\n"+`{
		"facebook": {"text": "A new article on slowing down."},
		"instagram": {"caption": "Slow colour, slow breath.", "hashtags": ["arttherapy", "#calm", "#calm", "#not a tag"]},
		"linkedin": {"text": "Reflecting on calm in practice."}
	}`+"\n```")
	got, err := c.Suggest(ctx, post(article, ""), assistant.Request{
		Channels: []string{"facebook", "instagram", "linkedin"}, Language: "en"})
	require.NoError(t, err)
	require.Equal(t, assistant.Suggestions{
		Facebook:  &assistant.Text{Text: "A new article on slowing down."},
		Instagram: &assistant.Instagram{Caption: "Slow colour, slow breath.", Hashtags: []string{"#arttherapy", "#calm"}},
		LinkedIn:  &assistant.Text{Text: "Reflecting on calm in practice."},
	}, got)

	require.Equal(t, "sk-test", f.header.Get("x-api-key"))
	require.Equal(t, "2023-06-01", f.header.Get("anthropic-version"))
	require.Equal(t, "application/json", f.header.Get("content-type"))
	require.Equal(t, "claude-haiku-4-5-20251001", f.body["model"])
	require.Contains(t, f.body["system"], "Never invent facts")
	prompt := f.body["messages"].([]any)[0].(map[string]any)["content"].(string)
	require.Contains(t, prompt, article)
	require.Contains(t, prompt, `"instagram"`)
	require.NotContains(t, prompt, `"website"`)
	require.NotContains(t, logs.String(), article)
	require.NotContains(t, logs.String(), "slowing down")
	require.NotContains(t, logs.String(), "sk-test")
}

// A translation of the website version goes from the other language into
// the one asked for; without text there, nothing is asked.
func TestTranslateWebsite(t *testing.T) {
	c, f, _ := newClient(t, `{"website": {"title": "ငြိမ်သက်မှု", "excerpt": "", "body": "ပန်းချီ"}}`)
	got, err := c.Suggest(ctx, post(article, ""), assistant.Request{Channels: []string{"website"}, Language: "my"})
	require.NoError(t, err)
	require.Equal(t, &assistant.Website{Title: "ငြိမ်သက်မှု", Body: "ပန်းချီ"}, got.Website)
	prompt := f.body["messages"].([]any)[0].(map[string]any)["content"].(string)
	require.Contains(t, prompt, article)
	require.Contains(t, prompt, "Burmese")

	_, err = c.Suggest(ctx, post(article, ""), assistant.Request{Channels: []string{"website"}, Language: "en"})
	requireCode(t, apperr.ActionNotAllowed, err) // no Burmese to translate from
	require.Equal(t, 1, f.calls)
}

func requireCode(t *testing.T, code apperr.Code, err error) {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, code, e.Code, e.Detail)
}

// Without a key the assistant is off; without website text it has nothing
// to work from; either way the model is not asked.
func TestSuggestRefusals(t *testing.T) {
	c, f, _ := newClient(t, "{}")
	_, err := c.Suggest(ctx, post("", ""), assistant.Request{Channels: []string{"facebook"}, Language: "en"})
	requireCode(t, apperr.ActionNotAllowed, err)
	c.APIKey = ""
	_, err = c.Suggest(ctx, post(article, ""), assistant.Request{Channels: []string{"facebook"}, Language: "en"})
	requireCode(t, apperr.FeatureUnavailable, err)
	require.False(t, c.Enabled())
	require.Zero(t, f.calls)
}

// An answer that is not JSON, misses a channel, breaks a platform limit or
// was cut short, or a model that does not answer, is ai_failed.
func TestUnusableAnswers(t *testing.T) {
	r := assistant.Request{Channels: []string{"linkedin"}, Language: "en"}
	for name, answer := range map[string]string{
		"not json":       "I cannot help with that.",
		"channel missed": `{"facebook": {"text": "Hi"}}`,
		"blank":          `{"linkedin": {"text": "  "}}`,
		"over 3,000":     `{"linkedin": {"text": "` + strings.Repeat("a", 3001) + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, _, _ := newClient(t, answer)
			_, err := c.Suggest(ctx, post(article, ""), r)
			requireCode(t, apperr.AIFailed, err)
		})
	}

	c, f, _ := newClient(t, `{"linkedin": {"text": "Hi"}}`)
	f.stopReason = "max_tokens"
	_, err := c.Suggest(ctx, post(article, ""), r)
	requireCode(t, apperr.AIFailed, err)

	f.status = http.StatusServiceUnavailable
	_, err = c.Suggest(ctx, post(article, ""), r)
	requireCode(t, apperr.AIFailed, err)

	ig := assistant.Request{Channels: []string{"instagram"}, Language: "en"}
	c, _, _ = newClient(t, `{"instagram": {"caption": "`+strings.Repeat("a", 2190)+`", "hashtags": ["#one", "#two"]}}`)
	_, err = c.Suggest(ctx, post(article, ""), ig)
	requireCode(t, apperr.AIFailed, err) // caption and hashtags together over 2,200
}
