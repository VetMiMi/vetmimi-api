package linkedin

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

const (
	platformName = "linkedin"
	// stateLifetime is how long Daw Mi has to sign in to LinkedIn.
	stateLifetime = 10 * time.Minute
	// warnBefore is how early the Connections page warns that the token is
	// about to expire; LinkedIn's last about 60 days and cannot be renewed
	// without her signing in again.
	warnBefore = 7 * 24 * time.Hour
	// scopes are the products "Sign In with LinkedIn using OpenID Connect"
	// (openid, profile: her member id and name) and "Share on LinkedIn".
	scopes     = "openid profile w_member_social"
	shareScope = "w_member_social"
)

var (
	errNotSetUp = apperr.New(apperr.Unavailable,
		"LinkedIn is not set up on the server: LINKEDIN_CLIENT_ID and LINKEDIN_CLIENT_SECRET are empty.")
	errState = apperr.New(apperr.ActionNotAllowed,
		"This connection attempt expired or was started by someone else; connect again.")
	errNoShare = apperr.New(apperr.ActionNotAllowed,
		"LinkedIn did not allow posting on your behalf; connect again and allow sharing.")
)

// Connection is the LinkedIn connection as the Connections page shows it.
// Status is not_connected, connected, expiring_soon (within warnBefore of
// ExpiresAt) or reconnect_required (expired, or refused by LinkedIn).
type Connection struct {
	Status      string
	MemberName  string
	ExpiresAt   sql.NullTime
	ConnectedAt time.Time
}

// AuthorizeURL is LinkedIn's sign-in dialog for user, which sends Daw Mi
// back to RedirectURL with a code and a signed state that only user can
// use, for stateLifetime.
func (c *Connector) AuthorizeURL(user pgtype.UUID, now time.Time) (string, error) {
	if c.ClientID == "" {
		return "", errNotSetUp
	}
	q := url.Values{
		"response_type": {"code"}, "client_id": {c.ClientID}, "redirect_uri": {c.RedirectURL}, "scope": {scopes},
		"state": {platform.SignOAuthState(c.SigningSecret, platformName, user.String(), now.Add(stateLifetime))},
	}
	return strings.TrimSuffix(c.AuthURL, "/") + "/oauth/v2/authorization?" + q.Encode(), nil
}

// Finish completes the sign-in for user: it trades code for an access
// token, reads her member id and name, and keeps the token, sealed, with
// its expiry.
func (c *Connector) Finish(ctx context.Context, user pgtype.UUID, code, state string, now time.Time) (Connection, error) {
	if c.ClientID == "" {
		return Connection{}, errNotSetUp
	}
	if !platform.CheckOAuthState(c.SigningSecret, platformName, state, user.String(), now) {
		return Connection{}, errState
	}
	form := url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {c.RedirectURL},
		"client_id": {c.ClientID}, "client_secret": {c.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.AuthURL, "/")+"/oauth/v2/accessToken", strings.NewReader(form.Encode()))
	if err != nil {
		return Connection{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Scope       string `json:"scope"`
	}
	if _, err := c.do(req, &token); err != nil {
		return Connection{}, c.refusal(ctx, err)
	}
	if !slices.Contains(strings.FieldsFunc(token.Scope, func(r rune) bool { return r == ',' || r == ' ' }), shareScope) {
		return Connection{}, errNoShare
	}
	var member struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	if req, err = c.rest(ctx, http.MethodGet, "/v2/userinfo", token.AccessToken, nil); err != nil {
		return Connection{}, err
	}
	if _, err := c.do(req, &member); err != nil {
		return Connection{}, c.refusal(ctx, err)
	}
	if member.Sub == "" {
		return Connection{}, errNoShare
	}
	sealed, err := c.Tokens.Seal([]byte(token.AccessToken))
	if err != nil {
		return Connection{}, err
	}
	params := db.SaveConnectionParams{Platform: platformName, Status: "connected", Token: sealed,
		AccountID: text(member.Sub), AccountName: text(member.Name), ConnectedBy: user, Now: now}
	if token.ExpiresIn > 0 {
		params.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(token.ExpiresIn) * time.Second), Valid: true}
	}
	if _, err := db.New(c.Pool).SaveConnection(ctx, params); err != nil {
		return Connection{}, err
	}
	return c.Status(ctx, now)
}

// Status reads the connection as it stands at now.
func (c *Connector) Status(ctx context.Context, now time.Time) (Connection, error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{Status: "not_connected"}, nil
	}
	if err != nil {
		return Connection{}, err
	}
	out := Connection{Status: "connected", MemberName: row.AccountName.String, ExpiresAt: row.ExpiresAt,
		ConnectedAt: row.ConnectedAt}
	switch {
	case row.LastError.String == "reconnect_required" || expired(row.ExpiresAt, now):
		out.Status = "reconnect_required"
	case row.ExpiresAt.Valid && row.ExpiresAt.Time.Sub(now) <= warnBefore:
		out.Status = "expiring_soon"
	}
	return out, nil
}

// Disconnect deletes the connection and its token; LinkedIn is then posted
// by hand. LinkedIn offers no way for the app to revoke a member's token.
func (c *Connector) Disconnect(ctx context.Context) error {
	return db.New(c.Pool).DeleteConnection(ctx, platformName)
}

// refusal turns a LinkedIn failure during the connection into what Daw Mi
// can act on.
func (c *Connector) refusal(ctx context.Context, err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.Status/100 == 4 {
		c.Log.WarnContext(ctx, "linkedin_refused", "status", ae.Status, "code", ae.Code, "error", ae.OAuth)
		return apperr.New(apperr.ActionNotAllowed, "LinkedIn refused the connection; connect again.")
	}
	c.Log.WarnContext(ctx, "linkedin_unreachable", "err", err)
	return apperr.New(apperr.Unavailable, "LinkedIn could not be reached; try again in a minute.")
}

func expired(at sql.NullTime, now time.Time) bool { return at.Valid && !now.Before(at.Time) }

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
