// Package meta connects Daw Mi's Facebook Page and the Instagram Business
// account linked to it, and posts the portal's facebook and instagram
// channel versions there through the Graph API (docs/meta-setup.md).
package meta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/config"
)

// Connector holds what the Meta connection and publishing need.
type Connector struct {
	Pool *pgxpool.Pool
	// Tokens seals the stored tokens with the AES-GCM key TOTP secrets use.
	Tokens *auth.TOTP
	// AppID and AppSecret are the Meta app's; empty, Meta is not set up.
	AppID, AppSecret string
	// ConfigID is the Facebook Login for Business configuration that names
	// the permissions; empty, the dialog asks for them as scopes instead.
	ConfigID string
	Version  string
	// GraphURL is https://graph.facebook.com, or a fake in tests.
	GraphURL string
	// RedirectURL is the admin Connections page Facebook sends Daw Mi back
	// to; it must be listed in the app's Valid OAuth Redirect URIs.
	RedirectURL string
	// SigningSecret signs the OAuth state.
	SigningSecret []byte
	// MediaPublicURL serves the web sizes the Graph API fetches images from.
	MediaPublicURL string
	// HTTP calls the Graph API; nil means a client with a 30-second timeout.
	HTTP *http.Client
	// PollEvery spaces the checks on an Instagram container; zero means
	// three seconds.
	PollEvery time.Duration
	Log       *slog.Logger
	Now       clock.Now
}

// New returns the connector cmd/api wires from cfg.
func New(cfg config.Config, pool *pgxpool.Pool, tokens *auth.TOTP, log *slog.Logger) *Connector {
	return &Connector{
		Pool: pool, Tokens: tokens, AppID: cfg.MetaAppID, AppSecret: cfg.MetaAppSecret,
		ConfigID: cfg.MetaConfigID, Version: cfg.MetaGraphVersion, GraphURL: "https://graph.facebook.com",
		RedirectURL:   strings.TrimSuffix(cfg.SiteURL, "/") + "/admin/settings/connections",
		SigningSecret: cfg.SigningSecret, MediaPublicURL: cfg.MediaPublicURL, Log: log, Now: time.Now,
	}
}

// graphError is the Graph API refusing a call, with its error code.
type graphError struct {
	Status  int
	Code    int
	Message string
}

func (e *graphError) Error() string {
	return fmt.Sprintf("meta: graph answered %d, code %d: %s", e.Status, e.Code, e.Message)
}

// call sends one Graph API request, as token when token is not empty: a GET
// with params in the query, or a POST with them as a form. out, if not nil,
// receives the JSON answer.
func (c *Connector) call(ctx context.Context, method, path, token string, params url.Values, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if token != "" {
		params.Set("access_token", token)
		params.Set("appsecret_proof", proof(token, c.AppSecret))
	}
	u := strings.TrimSuffix(c.GraphURL, "/") + "/" + c.Version + "/" + path
	var body io.Reader
	if method == http.MethodGet {
		u += "?" + params.Encode()
	} else {
		body = strings.NewReader(params.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		// The URL carries the token and the app secret: keep it out of an
		// error that will be logged.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("meta: %s %s: %w", method, path, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("meta: %s %s: %w", method, path, err)
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return &graphError{Status: res.StatusCode, Code: e.Error.Code, Message: e.Error.Message}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("meta: %s %s: %w", method, path, err)
	}
	return nil
}

// proof is the appsecret_proof Meta checks when the app requires it: a
// stolen token is useless without the app secret.
func proof(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
