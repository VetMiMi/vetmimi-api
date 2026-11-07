// Package assistant is the publishing portal's AI helper. Suggest drafts a
// post's social versions, or translates its website version, through the
// Gemini API. Nothing is saved; the editor accepts it in the portal.
package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/config"
)

var (
	errOff = apperr.New(apperr.FeatureUnavailable,
		"The AI assistant is off: GEMINI_API_KEY is empty on the server.")
	errFailed = apperr.New(apperr.AIFailed, "The AI assistant gave no usable answer; try again.")
	errQuota  = apperr.New(apperr.AIQuota, "The AI assistant has used its free allowance for now; try again later.")
)

type Client struct {
	APIKey string // empty: the assistant is off
	Model  string
	URL    string
	HTTP   *http.Client // nil: a client with a 90-second timeout
	Log    *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) *Client {
	return &Client{APIKey: cfg.GeminiAPIKey, Model: cfg.GeminiModel,
		URL: "https://generativelanguage.googleapis.com", Log: log}
}

func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

type part struct {
	Text string `json:"text"`
}

type contentParts struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

// answer is the part of a generateContent answer the assistant reads.
type answer struct {
	Candidates []struct {
		Content      contentParts `json:"content"`
		FinishReason string       `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokenCount     int `json:"promptTokenCount"`
		CandidatesTokenCount int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
	Error struct {
		Status string `json:"status"`
	} `json:"error"`
}

// ask sends one generateContent request and returns the answer's text. The
// log never holds the prompt or the answer.
func (c *Client) ask(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	req, err := c.request(ctx, system, prompt, maxTokens)
	if err != nil {
		return "", err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 90 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		c.Log.WarnContext(ctx, "assistant_unreachable", "err", err)
		return "", errFailed
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		c.Log.WarnContext(ctx, "assistant_unreachable", "err", err)
		return "", errFailed
	}
	var a answer
	_ = json.Unmarshal(raw, &a)
	if res.StatusCode != http.StatusOK {
		c.Log.WarnContext(ctx, "assistant_refused", "status", res.StatusCode, "error", a.Error.Status)
		if res.StatusCode == http.StatusTooManyRequests {
			return "", errQuota
		}
		return "", errFailed
	}
	if len(a.Candidates) == 0 {
		c.Log.WarnContext(ctx, "assistant_blocked", "model", c.Model)
		return "", errFailed
	}
	first := a.Candidates[0]
	c.Log.InfoContext(ctx, "assistant_answered", "model", c.Model, "finish_reason", first.FinishReason,
		"input_tokens", a.UsageMetadata.PromptTokenCount, "output_tokens", a.UsageMetadata.CandidatesTokenCount)
	if first.FinishReason != "STOP" {
		return "", errFailed
	}
	var text strings.Builder
	for _, p := range first.Content.Parts {
		text.WriteString(p.Text)
	}
	return text.String(), nil
}

func (c *Client) request(ctx context.Context, system, prompt string, maxTokens int) (*http.Request, error) {
	body, err := json.Marshal(map[string]any{
		"systemInstruction": contentParts{Parts: []part{{Text: system}}},
		"contents":          []contentParts{{Role: "user", Parts: []part{{Text: prompt}}}},
		"generationConfig":  map[string]any{"maxOutputTokens": maxTokens, "responseMimeType": "application/json"},
	})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimSuffix(c.URL, "/") + "/v1beta/models/" + url.PathEscape(c.Model) + ":generateContent"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", c.APIKey)
	req.Header.Set("content-type", "application/json")
	return req, nil
}
