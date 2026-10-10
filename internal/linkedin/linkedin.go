// Package linkedin connects Daw Mi's LinkedIn personal profile and posts the
// portal's linkedin version there through the Posts API.
// Flow: AuthorizeURL → Finish → Status → Publish.
package linkedin

import (
	"bytes"
	"context"
	"database/sql"
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
	// ClientID and ClientSecret are empty when LinkedIn is not set up.
	ClientID, ClientSecret string
	// Version is the LinkedIn-Version header the REST API requires (YYYYMM).
	Version string
	AuthURL string
	APIURL  string
	// RedirectURL must be listed in the LinkedIn app's Authorized redirect URLs.
	RedirectURL    string
	SigningSecret  []byte
	MediaPublicURL string
	HTTP           *http.Client // nil: a client with a 30-second timeout
	Log            *slog.Logger
	Now            clock.Now
}

func New(cfg config.Config, pool *pgxpool.Pool, tokens *secretbox.Box, log *slog.Logger) *Connector {
	return &Connector{
		Pool:           pool,
		Tokens:         tokens,
		ClientID:       cfg.LinkedInClientID,
		ClientSecret:   cfg.LinkedInClientSecret,
		Version:        cfg.LinkedInAPIVersion,
		AuthURL:        "https://www.linkedin.com",
		APIURL:         "https://api.linkedin.com",
		RedirectURL:    strings.TrimSuffix(cfg.SiteURL, "/") + "/admin/settings/connections/linkedin",
		SigningSecret:  cfg.SigningSecret,
		MediaPublicURL: cfg.MediaPublicURL,
		Log:            log,
		Now:            time.Now,
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

// rest builds a request to the versioned REST API, with body as JSON when it is not nil.
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

// do sends req. out, if not nil, receives the JSON answer. The headers are
// returned because a created post's id comes in X-Restli-Id.
func (c *Connector) do(req *http.Request, out any) (http.Header, error) {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	what := req.Method + " " + req.URL.Path
	res, err := client.Do(req)
	if err != nil {
		// An upload URL carries a signed ticket: keep it out of the logged error.
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

func expired(at sql.NullTime, now time.Time) bool { return at.Valid && !now.Before(at.Time) }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
