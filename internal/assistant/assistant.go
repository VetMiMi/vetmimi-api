// Package assistant is the publishing portal's AI helper: it suggests the
// social channel versions of a post, and translates its website version,
// from the post's own text, through the Anthropic Messages API. Nothing it
// suggests is saved; the editor edits and accepts it in the portal.
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

	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

const anthropicVersion = "2023-06-01"

var (
	errOff = apperr.New(apperr.FeatureUnavailable,
		"The AI assistant is off: ANTHROPIC_API_KEY is empty on the server.")
	errFailed = apperr.New(apperr.AIFailed, "The AI assistant gave no usable answer; try again.")
)

// Client asks Claude for suggestions.
type Client struct {
	// APIKey is empty when the assistant is off.
	APIKey string
	Model  string
	// URL is https://api.anthropic.com, or a fake in tests.
	URL string
	// HTTP calls the API; nil means a client with a 30-second timeout.
	HTTP *http.Client
	Log  *slog.Logger
}

// New returns the client cmd/api wires from cfg.
func New(cfg platform.Config, log *slog.Logger) *Client {
	return &Client{APIKey: cfg.AnthropicAPIKey, Model: cfg.AnthropicModel, URL: "https://api.anthropic.com", Log: log}
}

// Enabled reports whether the assistant can be asked.
func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ask sends one Messages API request and returns the text of the answer.
// Every failure is errFailed to the editor; the log keeps the status and
// error type only, never the prompt or the answer.
func (c *Client) ask(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	body, err := json.Marshal(map[string]any{
		"model": c.Model, "max_tokens": maxTokens, "system": system,
		"messages": []message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+"/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
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
	var answer struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &answer)
	if res.StatusCode != http.StatusOK {
		c.Log.WarnContext(ctx, "assistant_refused", "status", res.StatusCode, "type", answer.Error.Type)
		return "", errFailed
	}
	c.Log.InfoContext(ctx, "assistant_answered", "model", c.Model, "stop_reason", answer.StopReason,
		"input_tokens", answer.Usage.InputTokens, "output_tokens", answer.Usage.OutputTokens)
	if answer.StopReason != "end_turn" {
		return "", errFailed
	}
	var text strings.Builder
	for _, block := range answer.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return text.String(), nil
}
