// Package meta connects Daw Mi's Facebook Page and its linked Instagram
// account, then posts the portal's facebook and instagram versions there.
// Flow: AuthorizeURL → Finish (→ ChoosePage) → Status → PublishFacebook/PublishInstagram.
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

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/config"
	"github.com/VetMiMi/vetmimi-api/internal/secretbox"
)

type Connector struct {
	Pool   *pgxpool.Pool
	Tokens *secretbox.Box
	// AppID and AppSecret are empty when Meta is not set up.
	AppID, AppSecret string
	// ConfigID is the Facebook Login for Business configuration; empty, the dialog asks for scopes.
	ConfigID string
	Version  string
	GraphURL string
	// RedirectURL must be listed in the Meta app's Valid OAuth Redirect URIs.
	RedirectURL    string
	SigningSecret  []byte
	MediaPublicURL string
	HTTP           *http.Client  // nil: a client with a 30-second timeout
	PollEvery      time.Duration // between Instagram container checks; zero: 3 seconds
	Log            *slog.Logger
	Now            clock.Now
}

func New(cfg config.Config, pool *pgxpool.Pool, tokens *secretbox.Box, log *slog.Logger) *Connector {
	return &Connector{
		Pool:           pool,
		Tokens:         tokens,
		AppID:          cfg.MetaAppID,
		AppSecret:      cfg.MetaAppSecret,
		ConfigID:       cfg.MetaConfigID,
		Version:        cfg.MetaGraphVersion,
		GraphURL:       "https://graph.facebook.com",
		RedirectURL:    strings.TrimSuffix(cfg.SiteURL, "/") + "/admin/settings/connections",
		SigningSecret:  cfg.SigningSecret,
		MediaPublicURL: cfg.MediaPublicURL,
		Log:            log,
		Now:            time.Now,
	}
}

type graphError struct {
	Status  int
	Code    int
	Message string
}

func (e *graphError) Error() string {
	return fmt.Sprintf("meta: graph answered %d, code %d: %s", e.Status, e.Code, e.Message)
}

// call sends one Graph API request, signed with token when it is not empty.
// A GET carries params in the query, a POST as a form. out, if not nil,
// receives the JSON answer.
func (c *Connector) call(ctx context.Context, method, path, token string, params url.Values, out any) error {
	req, err := c.graphRequest(ctx, method, path, token, params)
	if err != nil {
		return err
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		// The URL carries the token and the app secret: keep it out of the logged error.
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
		return parseGraphError(res.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("meta: %s %s: %w", method, path, err)
	}
	return nil
}

func (c *Connector) graphRequest(ctx context.Context, method, path, token string, params url.Values) (*http.Request, error) {
	if params == nil {
		params = url.Values{}
	}
	if token != "" {
		params.Set("access_token", token)
		params.Set("appsecret_proof", proof(token, c.AppSecret))
	}
	u := strings.TrimSuffix(c.GraphURL, "/") + "/" + c.Version + "/" + path
	if method == http.MethodGet {
		return http.NewRequestWithContext(ctx, method, u+"?"+params.Encode(), nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

func parseGraphError(status int, raw []byte) *graphError {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(raw, &e)
	return &graphError{Status: status, Code: e.Error.Code, Message: e.Error.Message}
}

// proof is the appsecret_proof Meta checks: a stolen token is useless without the app secret.
func proof(token, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
