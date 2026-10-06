package meta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/media"
)

// pollTries bounds the wait for an Instagram container: with PollEvery's
// default, a minute, well inside the scheduler's stuckAfter.
const pollTries = 20

// Graph API error codes: an access token that is no longer valid, missing
// permissions (10, and 200 to 299), and the rate limits, which refuse a call
// before doing it.
const (
	codeToken      = 190
	codePermission = 10
)

var codeRateLimit = []int{4, 17, 32, 613, 80001, 80002}

// PublishFacebook posts v to the connected Page: its text with its link, or
// with its images attached, uploaded unpublished first.
func (c *Connector) PublishFacebook(ctx context.Context, v db.PostVersion) (content.Posted, error) {
	pageID, token, _, err := c.page(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	images, err := c.imageURLs(ctx, v.ImageIds)
	if err != nil {
		return content.Posted{}, err
	}
	post := url.Values{"message": {v.Text.String}}
	if len(images) == 0 && v.LinkUrl.Valid {
		post.Set("link", v.LinkUrl.String)
	} else if v.LinkUrl.Valid {
		// A post with photos takes no link preview, so the link goes in the text.
		post.Set("message", v.Text.String+"\n\n"+v.LinkUrl.String)
	}
	for i, image := range images {
		var photo struct {
			ID string `json:"id"`
		}
		err := c.call(ctx, http.MethodPost, pageID+"/photos", token,
			url.Values{"url": {image}, "published": {"false"}}, &photo)
		if err != nil {
			return content.Posted{}, c.failed(ctx, err, false)
		}
		attached, _ := json.Marshal(map[string]string{"media_fbid": photo.ID}) // a string map always marshals
		post.Set(fmt.Sprintf("attached_media[%d]", i), string(attached))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, pageID+"/feed", token, post, &created); err != nil {
		return content.Posted{}, c.failed(ctx, err, true)
	}
	return content.Posted{ExternalID: created.ID, Permalink: c.permalink(ctx, created.ID, token, "permalink_url")}, nil
}

// PublishInstagram posts v to the Instagram account linked to the Page: a
// container for its one image, or a carousel of up to ten, which Instagram
// must finish processing before it can be published.
func (c *Connector) PublishInstagram(ctx context.Context, v db.PostVersion) (content.Posted, error) {
	_, token, igID, err := c.page(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	if igID == "" {
		return content.Posted{}, content.ErrNotConnected
	}
	images, err := c.imageURLs(ctx, v.ImageIds)
	if err != nil {
		return content.Posted{}, err
	}
	caption := v.Text.String
	var container string
	if len(images) == 1 {
		container, err = c.container(ctx, igID, token, url.Values{"image_url": {images[0]}, "caption": {caption}})
	} else {
		children := make([]string, len(images))
		for i, image := range images {
			if children[i], err = c.container(ctx, igID, token,
				url.Values{"image_url": {image}, "is_carousel_item": {"true"}}); err != nil {
				break
			}
		}
		if err == nil {
			container, err = c.container(ctx, igID, token, url.Values{
				"media_type": {"CAROUSEL"}, "children": {strings.Join(children, ",")}, "caption": {caption},
			})
		}
	}
	if err == nil {
		err = c.waitFinished(ctx, container, token)
	}
	if err != nil {
		return content.Posted{}, err
	}
	var published struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, igID+"/media_publish", token,
		url.Values{"creation_id": {container}}, &published); err != nil {
		return content.Posted{}, c.failed(ctx, err, true)
	}
	return content.Posted{ExternalID: published.ID, Permalink: c.permalink(ctx, published.ID, token, "permalink")}, nil
}

func (c *Connector) container(ctx context.Context, igID, token string, params url.Values) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, igID+"/media", token, params, &created); err != nil {
		return "", c.failed(ctx, err, false)
	}
	return created.ID, nil
}

// waitFinished polls container until Instagram has fetched and processed
// its images. One it could not process is refused for good; one still in
// progress after pollTries is tried again later, as new containers.
func (c *Connector) waitFinished(ctx context.Context, container, token string) error {
	every := c.PollEvery
	if every == 0 {
		every = 3 * time.Second
	}
	for range pollTries {
		var status struct {
			StatusCode string `json:"status_code"`
		}
		if err := c.call(ctx, http.MethodGet, container, token,
			url.Values{"fields": {"status_code"}}, &status); err != nil {
			return c.failed(ctx, err, false)
		}
		switch status.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			c.Log.WarnContext(ctx, "meta_container_failed", "status", status.StatusCode)
			return &content.ChannelError{Reason: "rejected"}
		}
		select {
		case <-ctx.Done():
			return c.failed(ctx, ctx.Err(), false)
		case <-time.After(every):
		}
	}
	return &content.ChannelError{Reason: "connection_failed", Transient: true}
}

// permalink reads where a post that went out can be seen. The post is out
// either way, so a failure here only leaves the address empty.
func (c *Connector) permalink(ctx context.Context, id, token, field string) string {
	var out map[string]any
	if err := c.call(ctx, http.MethodGet, id, token, url.Values{"fields": {field}}, &out); err != nil {
		c.Log.WarnContext(ctx, "meta_permalink_unread", "err", err)
		return ""
	}
	link, _ := out[field].(string)
	return link
}

// page reads the connected Page's id, its token and the linked Instagram
// account's id. Without a connection the channel is not connected.
func (c *Connector) page(ctx context.Context) (pageID, token, igID string, err error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", content.ErrNotConnected
	}
	if err != nil {
		return "", "", "", err
	}
	if row.Status != "connected" {
		return "", "", "", content.ErrNotConnected
	}
	sealed, err := c.Tokens.Open(row.Token)
	if err != nil {
		return "", "", "", err
	}
	return row.AccountID.String, string(sealed), row.InstagramID.String, nil
}

// imageURLs are the public addresses of the largest web size of each image,
// which the Graph API fetches.
func (c *Connector) imageURLs(ctx context.Context, ids []pgtype.UUID) ([]string, error) {
	out := make([]string, len(ids))
	for i, id := range ids {
		m, err := db.New(c.Pool).GetMedia(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &content.ChannelError{Reason: "rejected"}
		}
		if err != nil {
			return nil, err
		}
		out[i] = media.Sizes(c.MediaPublicURL, m.ID, m.Widths)[0].URL
	}
	return out, nil
}

// failed says why a Graph API call did not post. A refusal is certain
// nothing went out; a lost answer to the call that posts (final) may hide a
// post that did, so it is never sent again on its own: Daw Mi checks the
// platform first (unknown_outcome).
func (c *Connector) failed(ctx context.Context, err error, final bool) error {
	var ge *graphError
	if !errors.As(err, &ge) || ge.Status/100 != 4 {
		c.Log.WarnContext(ctx, "meta_unreachable", "err", err, "final", final)
		if final {
			return &content.ChannelError{Reason: "unknown_outcome"}
		}
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	c.Log.WarnContext(ctx, "meta_refused", "code", ge.Code, "status", ge.Status, "message", ge.Message)
	switch {
	case ge.Code == codeToken || ge.Code == codePermission || ge.Code >= 200 && ge.Code < 300:
		c.markError(ctx, "reconnect_required")
		return &content.ChannelError{Reason: "reconnect_required"}
	case slices.Contains(codeRateLimit, ge.Code):
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	return &content.ChannelError{Reason: "rejected"}
}

// markError shows on the Connections page that the connection needs Daw Mi;
// failing to record it changes nothing about the channel's own failure.
func (c *Connector) markError(ctx context.Context, reason string) {
	err := db.New(c.Pool).SetConnectionError(ctx, db.SetConnectionErrorParams{
		Platform: platformName, LastError: text(reason), Now: c.Now(),
	})
	if err != nil {
		c.Log.WarnContext(ctx, "meta_error_unrecorded", "err", err)
	}
}
