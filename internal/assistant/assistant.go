// Package assistant is the publishing portal's AI helper. Suggest drafts a
// post's social versions, or translates its website version, through the
// Anthropic Messages API. Nothing is saved; the editor accepts it in the portal.
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

const anthropicVersion = "2023-06-01"

var (
	errOff = apperr.New(apperr.FeatureUnavailable,
		"The AI assistant is off: ANTHROPIC_API_KEY is empty on the server.")
	errFailed = apperr.New(apperr.AIFailed, "The AI assistant gave no usable answer; try again.")
)

type Client struct {
	APIKey string // empty: the assistant is off
	Model  string
	URL    string
	HTTP   *http.Client // nil: a client with a 30-second timeout
	Log    *slog.Logger
}

func New(cfg config.Config, log *slog.Logger) *Client {
	return &Client{APIKey: cfg.AnthropicAPIKey, Model: cfg.AnthropicModel, URL: "https://api.anthropic.com", Log: log}
}

func (c *Client) Enabled() bool { return c != nil && c.APIKey != "" }

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// messagesAnswer is the part of a Messages API answer the assistant reads.
type messagesAnswer struct {
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

// ask sends one Messages API request and returns the answer's text. Every
// failure is errFailed; the log never holds the prompt or the answer.
func (c *Client) ask(ctx context.Context, system, prompt string, maxTokens int) (string, error) {
	req, err := c.messagesRequest(ctx, system, prompt, maxTokens)
	if err != nil {
		return "", err
	}
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
	var answer messagesAnswer
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

func (c *Client) messagesRequest(ctx context.Context, system, prompt string, maxTokens int) (*http.Request, error) {
	body, err := json.Marshal(map[string]any{
		"model":      c.Model,
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   []message{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.URL, "/")+"/v1/messages",
		bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")
	return req, nil
}
