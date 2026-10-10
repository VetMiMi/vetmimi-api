// Package linkedin connects Daw Mi's LinkedIn personal profile and posts the
// portal's linkedin channel version there through the Posts API
// (docs/linkedin-setup.md).
package linkedin

import (
	"bytes"
	"context"
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

// Connector holds what the LinkedIn connection and publishing need.
type Connector struct {
	Pool *pgxpool.Pool
	// Tokens seals the stored token with the AES-GCM key TOTP secrets use.
	Tokens *auth.TOTP
	// ClientID and ClientSecret are the LinkedIn app's; empty, LinkedIn is
	// not set up.
	ClientID, ClientSecret string
	// Version is the LinkedIn-Version header the REST API requires (YYYYMM).
	Version string
	// AuthURL is https://www.linkedin.com and APIURL https://api.linkedin.com,
	// or a fake in tests.
	AuthURL, APIURL string
	// RedirectURL is the admin page LinkedIn sends Daw Mi back to; it must be
	// listed in the app's Authorized redirect URLs.
	RedirectURL string
	// SigningSecret signs the OAuth state.
	SigningSecret []byte
	// MediaPublicURL serves the web sizes uploaded to LinkedIn.
	MediaPublicURL string
	// HTTP calls LinkedIn; nil means a client with a 30-second timeout.
	HTTP *http.Client
	Log  *slog.Logger
	Now  clock.Now
}

// New returns the connector cmd/api wires from cfg.
func New(cfg config.Config, pool *pgxpool.Pool, tokens *auth.TOTP, log *slog.Logger) *Connector {
	return &Connector{
		Pool: pool, Tokens: tokens, ClientID: cfg.LinkedInClientID, ClientSecret: cfg.LinkedInClientSecret,
		Version: cfg.LinkedInAPIVersion, AuthURL: "https://www.linkedin.com", APIURL: "https://api.linkedin.com",
		RedirectURL:   strings.TrimSuffix(cfg.SiteURL, "/") + "/admin/settings/connections/linkedin",
		SigningSecret: cfg.SigningSecret, MediaPublicURL: cfg.MediaPublicURL, Log: log, Now: time.Now,
	}
}

// apiError is LinkedIn refusing a call. Code is the REST API's
// serviceErrorCode, OAuth the token endpoint's error name.
type apiError struct {
	Status int
	Code   int
	OAuth  string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("linkedin: answered %d, code %d %s", e.Status, e.Code, e.OAuth)
}

// rest is a request to the versioned REST API under APIURL, as token, with
// body as JSON when it is not nil.
func (c *Connector) rest(ctx context.Context, method, path, token string, body any) (*http.Request, error) {
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(c.APIURL, "/")+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("LinkedIn-Version", c.Version)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// do sends req. out, if not nil, receives the JSON answer; the headers are
// returned, since a created post's id comes in x-restli-id.
func (c *Connector) do(req *http.Request, out any) (http.Header, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	what := req.Method + " " + req.URL.Path
	res, err := client.Do(req)
	if err != nil {
		// An upload URL carries a signed ticket: keep it out of a logged error.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("linkedin: %s: %w", what, err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("linkedin: %s: %w", what, err)
	}
	if res.StatusCode/100 != 2 {
		var e struct {
			Code  int    `json:"serviceErrorCode"`
			OAuth string `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		return nil, &apiError{Status: res.StatusCode, Code: e.Code, OAuth: e.OAuth}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("linkedin: %s: %w", what, err)
		}
	}
	return res.Header, nil
}
