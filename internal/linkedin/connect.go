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

// Connection is what the Connections page shows. Status is not_connected,
// connected, expiring_soon (within warnBefore of ExpiresAt) or
// reconnect_required (expired, or refused by LinkedIn).
type Connection struct {
	Status      string
	MemberName  string
	ExpiresAt   sql.NullTime
	ConnectedAt time.Time
}

type accessToken struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
	Scope       string `json:"scope"`
}

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

// AuthorizeURL is LinkedIn's sign-in dialog. Its signed state is valid only
// for user, for stateLifetime.
func (connector *Connector) AuthorizeURL(user pgtype.UUID, now time.Time) (string, error) {
	if connector.ClientID == "" {
		return "", errNotSetUp
	}
	state := tokens.SignOAuthState(connector.SigningSecret, platformName, user.String(), now.Add(stateLifetime))
	query := url.Values{
		"response_type": {"code"},
		"client_id":     {connector.ClientID},
		"redirect_uri":  {connector.RedirectURL},
		"scope":         {scopes},
		"state":         {state},
	}
	return strings.TrimSuffix(connector.AuthURL, "/") + "/oauth/v2/authorization?" + query.Encode(), nil
}

// Finish trades code for an access token, reads the member's id and name,
// and stores the token with its expiry.
func (connector *Connector) Finish(ctx context.Context, user pgtype.UUID, code, state string, now time.Time) (Connection, error) {
	if connector.ClientID == "" {
		return Connection{}, errNotSetUp
	}
	if !tokens.CheckOAuthState(connector.SigningSecret, platformName, state, user.String(), now) {
		return Connection{}, errState
	}
	token, err := connector.exchangeCode(ctx, code)
	if err != nil {
		return Connection{}, err
	}
	if !hasScope(token.Scope, shareScope) {
		return Connection{}, errNoShare
	}
	memberID, memberName, err := connector.userInfo(ctx, token.AccessToken)
	if err != nil {
		return Connection{}, err
	}

	sealed, err := connector.Tokens.Seal([]byte(token.AccessToken))
	if err != nil {
		return Connection{}, err
	}
	params := db.SaveConnectionParams{
		Platform:    platformName,
		Status:      "connected",
		Token:       sealed,
		AccountID:   text(memberID),
		AccountName: text(memberName),
		ConnectedBy: user,
		Now:         now,
	}
	if token.ExpiresIn > 0 {
		params.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(token.ExpiresIn) * time.Second), Valid: true}
	}
	_, err = db.New(connector.Pool).SaveConnection(ctx, params)
	if err != nil {
		return Connection{}, err
	}
	return connector.Status(ctx, now)
}

func (connector *Connector) Status(ctx context.Context, now time.Time) (Connection, error) {
	row, err := db.New(connector.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{Status: "not_connected"}, nil
	}
	if err != nil {
		return Connection{}, err
	}
	connection := Connection{
		Status:      "connected",
		MemberName:  row.AccountName.String,
		ExpiresAt:   row.ExpiresAt,
		ConnectedAt: row.ConnectedAt,
	}
	switch {
	case row.LastError.String == "reconnect_required" || expired(row.ExpiresAt, now):
		connection.Status = "reconnect_required"
	case row.ExpiresAt.Valid && row.ExpiresAt.Time.Sub(now) <= warnBefore:
		connection.Status = "expiring_soon"
	}
	return connection, nil
}

// Disconnect deletes the connection and its token; LinkedIn is then posted
// by hand. LinkedIn offers no way for the app to revoke a member's token.
func (connector *Connector) Disconnect(ctx context.Context) error {
	return db.New(connector.Pool).DeleteConnection(ctx, platformName)
}

func (connector *Connector) exchangeCode(ctx context.Context, code string) (accessToken, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {connector.RedirectURL},
		"client_id":     {connector.ClientID},
		"client_secret": {connector.ClientSecret},
	}
	endpoint := strings.TrimSuffix(connector.AuthURL, "/") + "/oauth/v2/accessToken"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return accessToken{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token accessToken
	_, err = connector.do(req, &token)
	if err != nil {
		return accessToken{}, connector.refusal(ctx, err)
	}
	return token, nil
}

// userInfo reads the signed-in member's id and name. Without an id she cannot post.
func (connector *Connector) userInfo(ctx context.Context, token string) (id, name string, err error) {
	req, err := connector.restRequest(ctx, http.MethodGet, "/v2/userinfo", token, nil)
	if err != nil {
		return "", "", err
	}
	var info struct {
		Sub  string `json:"sub"`
		Name string `json:"name"`
	}
	_, err = connector.do(req, &info)
	if err != nil {
		return "", "", connector.refusal(ctx, err)
	}
	if info.Sub == "" {
		return "", "", errNoShare
	}
	return info.Sub, info.Name, nil
}

// hasScope reports whether granted, a comma- or space-separated list, holds scope.
func hasScope(granted, scope string) bool {
	separator := func(char rune) bool { return char == ',' || char == ' ' }
	return slices.Contains(strings.FieldsFunc(granted, separator), scope)
}

// refusal turns a LinkedIn failure while connecting into an error Daw Mi can act on.
func (connector *Connector) refusal(ctx context.Context, err error) error {
	var refused *apiError
	if errors.As(err, &refused) && refused.Status/100 == 4 {
		connector.Log.WarnContext(ctx, "linkedin_refused", "status", refused.Status, "code", refused.Code, "error", refused.OAuth)
		return apperr.New(apperr.ActionNotAllowed, "LinkedIn refused the connection; connect again.")
	}
	connector.Log.WarnContext(ctx, "linkedin_unreachable", "err", err)
	return apperr.New(apperr.Unavailable, "LinkedIn could not be reached; try again in a minute.")
}
