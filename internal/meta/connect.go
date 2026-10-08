package meta

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

const (
	platformName = "meta"
	// stateLifetime is how long Daw Mi has to sign in to Facebook.
	stateLifetime = 10 * time.Minute
	dialogURL     = "https://www.facebook.com"
	// scopes are what publishing needs (docs/meta-setup.md);
	// business_management covers a Page owned by a business portfolio.
	scopes = "pages_show_list,pages_manage_posts,pages_read_engagement,instagram_basic," +
		"instagram_content_publish,business_management"
)

var (
	errNotSetUp = apperr.New(apperr.Unavailable,
		"Meta is not set up on the server: META_APP_ID and META_APP_SECRET are empty.")
	errState = apperr.New(apperr.ActionNotAllowed,
		"This connection attempt expired or was started by someone else; connect again.")
	errNotChoosing = apperr.New(apperr.ActionNotAllowed, "Connect to Facebook before choosing a Page.")
	errNoSuchPage  = apperr.New(apperr.NotFound, "None of the Pages you manage has this id.")
)

// Connection is the Meta connection as the Connections page shows it.
// Pages are the ones Daw Mi manages, while she chooses one.
type Connection struct {
	Status            string // not_connected, choosing_page or connected
	PageID, PageName  string
	InstagramID       string
	InstagramUsername string
	ExpiresAt         sql.NullTime
	LastError         string
	ConnectedAt       time.Time
	Pages             []Page
}

// Page is a Facebook Page Daw Mi manages, with the Instagram Business
// account linked to it, if any.
type Page struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	AccessToken string `json:"access_token"`
	Instagram   *struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	} `json:"instagram_business_account"`
}

// AuthorizeURL is the Facebook Login dialog for user, which sends Daw Mi
// back to RedirectURL with a code and a signed state that only user can
// use, for stateLifetime.
func (c *Connector) AuthorizeURL(user pgtype.UUID, now time.Time) (string, error) {
	if c.AppID == "" {
		return "", errNotSetUp
	}
	q := url.Values{
		"client_id": {c.AppID}, "redirect_uri": {c.RedirectURL}, "response_type": {"code"},
		"state": {platform.SignOAuthState(c.SigningSecret, platformName, user.String(), now.Add(stateLifetime))},
	}
	if c.ConfigID != "" {
		q.Set("config_id", c.ConfigID)
	} else {
		q.Set("scope", scopes)
	}
	return dialogURL + "/" + c.Version + "/dialog/oauth?" + q.Encode(), nil
}

// Finish completes Facebook Login for user: it trades code for a
// long-lived user token and keeps it, sealed, while Daw Mi chooses her
// Page. With exactly one Page, that Page is chosen at once.
func (c *Connector) Finish(ctx context.Context, user pgtype.UUID, code, st string, now time.Time) (Connection, error) {
	if c.AppID == "" {
		return Connection{}, errNotSetUp
	}
	if !platform.CheckOAuthState(c.SigningSecret, platformName, st, user.String(), now) {
		return Connection{}, errState
	}
	var short, long struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	err := c.call(ctx, http.MethodGet, "oauth/access_token", "", url.Values{
		"client_id": {c.AppID}, "client_secret": {c.AppSecret}, "redirect_uri": {c.RedirectURL}, "code": {code},
	}, &short)
	if err == nil {
		err = c.call(ctx, http.MethodGet, "oauth/access_token", "", url.Values{
			"grant_type": {"fb_exchange_token"}, "client_id": {c.AppID}, "client_secret": {c.AppSecret},
			"fb_exchange_token": {short.AccessToken},
		}, &long)
	}
	if err != nil {
		return Connection{}, c.refusal(ctx, err)
	}
	pages, err := c.pages(ctx, long.AccessToken)
	if err != nil {
		return Connection{}, err
	}
	if len(pages) == 1 {
		return c.connect(ctx, user, pages[0], now)
	}
	sealed, err := c.Tokens.Seal([]byte(long.AccessToken))
	if err != nil {
		return Connection{}, err
	}
	params := db.SaveConnectionParams{Platform: platformName, Status: "choosing_page", Token: sealed,
		ConnectedBy: user, Now: now}
	if long.ExpiresIn > 0 {
		params.ExpiresAt = sql.NullTime{Time: now.Add(time.Duration(long.ExpiresIn) * time.Second), Valid: true}
	}
	if _, err := db.New(c.Pool).SaveConnection(ctx, params); err != nil {
		return Connection{}, err
	}
	return c.Status(ctx)
}

// ChoosePage connects the Page pageID, one Daw Mi manages, with its
// Instagram account.
func (c *Connector) ChoosePage(ctx context.Context, user pgtype.UUID, pageID string, now time.Time) (Connection, error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{}, errNotChoosing
	}
	if err != nil {
		return Connection{}, err
	}
	if row.Status != "choosing_page" {
		return Connection{}, errNotChoosing
	}
	token, err := c.Tokens.Open(row.Token)
	if err != nil {
		return Connection{}, err
	}
	pages, err := c.pages(ctx, string(token))
	if err != nil {
		return Connection{}, err
	}
	for _, p := range pages {
		if p.ID == pageID {
			return c.connect(ctx, user, p, now)
		}
	}
	return Connection{}, errNoSuchPage
}

// connect stores page's own token, sealed, which replaces the user token.
// A Page token from a long-lived user token does not expire, but Meta stops
// its data access after 90 days without Daw Mi signing in again.
func (c *Connector) connect(ctx context.Context, user pgtype.UUID, page Page, now time.Time) (Connection, error) {
	var debug struct {
		Data struct {
			ExpiresAt           int64 `json:"expires_at"`
			DataAccessExpiresAt int64 `json:"data_access_expires_at"`
		} `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, "debug_token", "", url.Values{
		"input_token": {page.AccessToken}, "access_token": {c.AppID + "|" + c.AppSecret},
	}, &debug)
	if err != nil {
		return Connection{}, c.refusal(ctx, err)
	}
	sealed, err := c.Tokens.Seal([]byte(page.AccessToken))
	if err != nil {
		return Connection{}, err
	}
	params := db.SaveConnectionParams{Platform: platformName, Status: "connected", Token: sealed,
		AccountID: text(page.ID), AccountName: text(page.Name), ConnectedBy: user, Now: now}
	if page.Instagram != nil {
		params.InstagramID, params.InstagramUsername = text(page.Instagram.ID), text(page.Instagram.Username)
	}
	for _, at := range []int64{debug.Data.ExpiresAt, debug.Data.DataAccessExpiresAt} {
		if at > 0 && (!params.ExpiresAt.Valid || at < params.ExpiresAt.Time.Unix()) {
			params.ExpiresAt = sql.NullTime{Time: time.Unix(at, 0).UTC(), Valid: true}
		}
	}
	if _, err := db.New(c.Pool).SaveConnection(ctx, params); err != nil {
		return Connection{}, err
	}
	return c.Status(ctx)
}

// pages lists the Pages the user token manages, with their tokens.
func (c *Connector) pages(ctx context.Context, userToken string) ([]Page, error) {
	var res struct {
		Data []Page `json:"data"`
	}
	err := c.call(ctx, http.MethodGet, "me/accounts", userToken, url.Values{
		"fields": {"id,name,access_token,instagram_business_account{id,username}"}, "limit": {"100"},
	}, &res)
	if err != nil {
		return nil, c.refusal(ctx, err)
	}
	return res.Data, nil
}

// Status reads the connection; while Daw Mi chooses her Page it lists the
// Pages she manages.
func (c *Connector) Status(ctx context.Context) (Connection, error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Connection{Status: "not_connected"}, nil
	}
	if err != nil {
		return Connection{}, err
	}
	out := Connection{Status: row.Status, PageID: row.AccountID.String, PageName: row.AccountName.String,
		InstagramID: row.InstagramID.String, InstagramUsername: row.InstagramUsername.String,
		ExpiresAt: row.ExpiresAt, LastError: row.LastError.String, ConnectedAt: row.ConnectedAt}
	if row.Status == "choosing_page" {
		token, err := c.Tokens.Open(row.Token)
		if err != nil {
			return Connection{}, err
		}
		if out.Pages, err = c.pages(ctx, string(token)); err != nil {
			return Connection{}, err
		}
	}
	return out, nil
}

// Disconnect deletes the connection and its token; Facebook and Instagram
// are then posted by hand.
func (c *Connector) Disconnect(ctx context.Context) error {
	return db.New(c.Pool).DeleteConnection(ctx, platformName)
}

// refusal turns a Graph API failure during the connection into what Daw Mi
// can act on: Facebook's own words for a refusal, or try again later.
func (c *Connector) refusal(ctx context.Context, err error) error {
	var ge *graphError
	if errors.As(err, &ge) && ge.Status/100 == 4 {
		c.Log.WarnContext(ctx, "meta_refused", "code", ge.Code, "status", ge.Status)
		return apperr.New(apperr.ActionNotAllowed, "Facebook refused the connection: "+ge.Message+
			" (code "+strconv.Itoa(ge.Code)+"). Connect again.")
	}
	c.Log.WarnContext(ctx, "meta_unreachable", "err", err)
	return apperr.New(apperr.Unavailable, "Facebook could not be reached; try again in a minute.")
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }
