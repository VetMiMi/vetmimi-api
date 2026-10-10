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

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

const (
	platformName  = "linkedin"
	stateLifetime = 10 * time.Minute
	// warnBefore is how early the Connections page warns of expiry. A token
	// lasts about 60 days and renews only by signing in again.
	warnBefore = 7 * 24 * time.Hour
	// openid and profile give the member id and name; w_member_social allows posting.
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

// Connection is what the Connections page shows. Status is not_connected,
// connected, expiring_soon (within warnBefore of ExpiresAt) or
// reconnect_required (expired, or refused by LinkedIn).
type Connection struct {
	Status      string
	MemberName  string
	ExpiresAt   sql.NullTime
	ConnectedAt time.Time
}

// AuthorizeURL is LinkedIn's sign-in dialog. Its signed state is valid only
// for user, for stateLifetime.
func (c *Connector) AuthorizeURL(user pgtype.UUID, now time.Time) (string, error) {
	if c.ClientID == "" {
		return "", errNotSetUp
	}
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {c.ClientID},
		"redirect_uri":  {c.RedirectURL},
		"scope":         {scopes},
		"state":         {tokens.SignOAuthState(c.SigningSecret, platformName, user.String(), now.Add(stateLifetime))},
	}
	return strings.TrimSuffix(c.AuthURL, "/") + "/oauth/v2/authorization?" + q.Encode(), nil
}

// Finish trades code for an access token, reads the member's id and name,
// and stores the token with its expiry.
func (c *Connector) Finish(ctx context.Context, user pgtype.UUID, code, state string, now time.Time) (Connection, error) {
	if c.ClientID == "" {
		return Connection{}, errNotSetUp
	}
	if !tokens.CheckOAuthState(c.SigningSecret, platformName, state, user.String(), now) {
		return Connection{}, errState
	}
	token, err := c.exchangeCode(ctx, code)
	if err != nil {
		return Connection{}, err
	}
	if !hasScope(token.Scope, shareScope) {
		return Connection{}, errNoShare
	}
	memberID, memberName, err := c.userInfo(ctx, token.AccessToken)
	if err != nil {
		return Connection{}, err
	}
	sealed, err := c.Tokens.Seal([]byte(token.AccessToken))
	if err != nil {
		return Connection{}, err
	}
	params := db.SaveConnectionParams{
		Platform: platformName, Status: "connected", Token: sealed,
		AccountID: text(memberID), AccountName: text(memberName), ConnectedBy: user, Now: now,
	}
	if token.ExpiresIn > 0 {
		params.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(token.ExpiresIn) * time.Second), Valid: true}
	}
	if _, err := db.New(c.Pool).SaveConnection(ctx, params); err != nil {
		return Connection{}, err
	}
	return c.Status(ctx, now)
}

type accessToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

func (c *Connector) exchangeCode(ctx context.Context, code string) (accessToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.RedirectURL},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimSuffix(c.AuthURL, "/")+"/oauth/v2/accessToken", strings.NewReader(form.Encode()))
	if err != nil {
		return accessToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token accessToken
	if _, err := c.do(req, &token); err != nil {
		return accessToken{}, c.refusal(ctx, err)
	}
	return token, nil
}

// userInfo reads the signed-in member's id and name. Without an id she cannot post.
func (c *Connector) userInfo(ctx context.Context, token string) (id, name string, err error) {
	req, err := c.rest(ctx, http.MethodGet, "/v2/userinfo", token, nil)
	if err != nil {
		return "", "", err
	}
	var info struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	if _, err := c.do(req, &info); err != nil {
		return "", "", c.refusal(ctx, err)
	}
	if info.Sub == "" {
		return "", "", errNoShare
	}
	return info.Sub, info.Name, nil
}

// hasScope reports whether granted, a comma- or space-separated list, holds scope.
func hasScope(granted, scope string) bool {
	return slices.Contains(strings.FieldsFunc(granted, func(r rune) bool { return r == ',' || r == ' ' }), scope)
}

func (c *Connector) Status(ctx context.Context, now time.Time) (Connection, error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{Status: "not_connected"}, nil
	}
	if err != nil {
		return Connection{}, err
	}
	out := Connection{
		Status:      "connected",
		MemberName:  row.AccountName.String,
		ExpiresAt:   row.ExpiresAt,
		ConnectedAt: row.ConnectedAt,
	}
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

// refusal turns a LinkedIn failure while connecting into an error Daw Mi can act on.
func (c *Connector) refusal(ctx context.Context, err error) error {
	var ae *apiError
	if errors.As(err, &ae) && ae.Status/100 == 4 {
		c.Log.WarnContext(ctx, "linkedin_refused", "status", ae.Status, "code", ae.Code, "error", ae.OAuth)
		return apperr.New(apperr.ActionNotAllowed, "LinkedIn refused the connection; connect again.")
	}
	c.Log.WarnContext(ctx, "linkedin_unreachable", "err", err)
	return apperr.New(apperr.Unavailable, "LinkedIn could not be reached; try again in a minute.")
}
