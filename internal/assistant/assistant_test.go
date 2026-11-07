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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

var ctx = context.Background()

// fakeGemini answers every generateContent call the same way and keeps the last request.
type fakeGemini struct {
	status       int
	answer       string
	finishReason string
	calls        int
	path         string
	header       http.Header
	body         map[string]any
}

func (f *fakeGemini) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.path = r.URL.Path
	f.header = r.Header.Clone()
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &f.body)
	if f.status != http.StatusOK {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(`{"error": {"code": 503, "message": "Overloaded", "status": "UNAVAILABLE"}}`))
		return
	}
	out, _ := json.Marshal(map[string]any{
		"candidates": []map[string]any{{
			"content":      map[string]any{"role": "model", "parts": []map[string]string{{"text": f.answer}}},
			"finishReason": f.finishReason,
		}},
		"usageMetadata": map[string]int{"promptTokenCount": 10, "candidatesTokenCount": 20},
	})
	_, _ = w.Write(out)
}

func newClient(t *testing.T, answer string) (*assistant.Client, *fakeGemini, *bytes.Buffer) {
	t.Helper()
	f := &fakeGemini{status: http.StatusOK, answer: answer, finishReason: "STOP"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	return &assistant.Client{APIKey: "test-key", Model: "gemini-flash-latest", URL: srv.URL,
		Log: slog.New(slog.NewJSONHandler(logs, nil))}, f, logs
}

// sent is the text of one part of the request body, e.g. the system instruction.
func sent(body map[string]any, key string) string {
	c := body[key]
	if list, ok := c.([]any); ok {
		c = list[0]
	}
	return c.(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
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

// The answer's JSON is found inside prose and hashtags are tidied. The log
// keeps neither the prompt nor the answer.
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

	require.Equal(t, "/v1beta/models/gemini-flash-latest:generateContent", f.path)
	require.Equal(t, "test-key", f.header.Get("x-goog-api-key"))
	require.Equal(t, "application/json", f.header.Get("content-type"))
	require.Contains(t, sent(f.body, "systemInstruction"), "Never invent facts")
	prompt := sent(f.body, "contents")
	require.Contains(t, prompt, article)
	require.Contains(t, prompt, `"instagram"`)
	require.NotContains(t, prompt, `"website"`)
	require.NotContains(t, logs.String(), article)
	require.NotContains(t, logs.String(), "slowing down")
	require.NotContains(t, logs.String(), "test-key")
}

// A translation goes from the other language; without text there, nothing is asked.
func TestTranslateWebsite(t *testing.T) {
	c, f, _ := newClient(t, `{"website": {"title": "ငြိမ်သက်မှု", "excerpt": "", "body": "ပန်းချီ"}}`)
	got, err := c.Suggest(ctx, post(article, ""), assistant.Request{Channels: []string{"website"}, Language: "my"})
	require.NoError(t, err)
	require.Equal(t, &assistant.Website{Title: "ငြိမ်သက်မှု", Body: "ပန်းချီ"}, got.Website)
	prompt := sent(f.body, "contents")
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

// Without a key or website text, the model is not asked.
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

// Every unusable answer is ai_failed.
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
	f.finishReason = "MAX_TOKENS"
	_, err := c.Suggest(ctx, post(article, ""), r)
	requireCode(t, apperr.AIFailed, err)

	f.status = http.StatusServiceUnavailable
	_, err = c.Suggest(ctx, post(article, ""), r)
	requireCode(t, apperr.AIFailed, err)

	f.status = http.StatusTooManyRequests
	_, err = c.Suggest(ctx, post(article, ""), r)
	requireCode(t, apperr.AIQuota, err)

	ig := assistant.Request{Channels: []string{"instagram"}, Language: "en"}
	c, _, _ = newClient(t, `{"instagram": {"caption": "`+strings.Repeat("a", 2190)+`", "hashtags": ["#one", "#two"]}}`)
	_, err = c.Suggest(ctx, post(article, ""), ig)
	requireCode(t, apperr.AIFailed, err) // caption and hashtags together over 2,200
}
