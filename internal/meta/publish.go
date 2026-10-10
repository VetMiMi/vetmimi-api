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

// pollTries keeps the wait to about a minute, inside the content scheduler's stuck limit.
const pollTries = 20

// Graph API error codes. Codes 200 to 299 are missing permissions too.
const (
	codeInvalidToken = 190
	codePermission   = 10
)

var codesRateLimit = []int{4, 17, 32, 613, 80001, 80002}

type connectedPage struct {
	ID, Token, InstagramID string
}

// PublishFacebook posts v to the Page: its text with its link, or with its
// images, which are uploaded unpublished first.
func (c *Connector) PublishFacebook(ctx context.Context, v db.PostVersion) (content.Posted, error) {
	page, err := c.page(ctx)
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
		// A post with photos has no link preview, so the link goes in the text.
		post.Set("message", v.Text.String+"\n\n"+v.LinkUrl.String)
	}
	for i, image := range images {
		photoID, err := c.uploadPhoto(ctx, page, image)
		if err != nil {
			return content.Posted{}, err
		}
		attached, _ := json.Marshal(map[string]string{"media_fbid": photoID}) // a string map always marshals
		post.Set(fmt.Sprintf("attached_media[%d]", i), string(attached))
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, page.ID+"/feed", page.Token, post, &created); err != nil {
		return content.Posted{}, c.failed(ctx, err, true)
	}
	return content.Posted{
		ExternalID: created.ID,
		Permalink:  c.permalink(ctx, created.ID, page.Token, "permalink_url"),
	}, nil
}

func (c *Connector) uploadPhoto(ctx context.Context, page connectedPage, imageURL string) (string, error) {
	var photo struct {
		ID string `json:"id"`
	}
	err := c.call(ctx, http.MethodPost, page.ID+"/photos", page.Token,
		url.Values{"url": {imageURL}, "published": {"false"}}, &photo)
	if err != nil {
		return "", c.failed(ctx, err, false)
	}
	return photo.ID, nil
}

// PublishInstagram posts v to the Instagram account linked to the Page: one
// image, or a carousel of up to ten. Instagram must finish processing the
// container before it can be published.
func (c *Connector) PublishInstagram(ctx context.Context, v db.PostVersion) (content.Posted, error) {
	page, err := c.page(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	if page.InstagramID == "" {
		return content.Posted{}, content.ErrNotConnected
	}
	images, err := c.imageURLs(ctx, v.ImageIds)
	if err != nil {
		return content.Posted{}, err
	}
	container, err := c.instagramContainer(ctx, page, images, v.Text.String)
	if err != nil {
		return content.Posted{}, err
	}
	if err := c.waitFinished(ctx, container, page.Token); err != nil {
		return content.Posted{}, err
	}
	var published struct {
		ID string `json:"id"`
	}
	err = c.call(ctx, http.MethodPost, page.InstagramID+"/media_publish", page.Token,
		url.Values{"creation_id": {container}}, &published)
	if err != nil {
		return content.Posted{}, c.failed(ctx, err, true)
	}
	return content.Posted{
		ExternalID: published.ID,
		Permalink:  c.permalink(ctx, published.ID, page.Token, "permalink"),
	}, nil
}

// instagramContainer creates the media container for one image, or a
// carousel container over one child container per image.
func (c *Connector) instagramContainer(ctx context.Context, page connectedPage, images []string, caption string) (string, error) {
	if len(images) == 1 {
		return c.container(ctx, page, url.Values{"image_url": {images[0]}, "caption": {caption}})
	}
	children := make([]string, len(images))
	for i, image := range images {
		child, err := c.container(ctx, page, url.Values{"image_url": {image}, "is_carousel_item": {"true"}})
		if err != nil {
			return "", err
		}
		children[i] = child
	}
	return c.container(ctx, page, url.Values{
		"media_type": {"CAROUSEL"},
		"children":   {strings.Join(children, ",")},
		"caption":    {caption},
	})
}

func (c *Connector) container(ctx context.Context, page connectedPage, params url.Values) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	if err := c.call(ctx, http.MethodPost, page.InstagramID+"/media", page.Token, params, &created); err != nil {
		return "", c.failed(ctx, err, false)
	}
	return created.ID, nil
}

// waitFinished polls container until Instagram has processed its images.
// One it could not process is rejected for good; one still in progress
// after pollTries is tried again later, with new containers.
func (c *Connector) waitFinished(ctx context.Context, container, token string) error {
	every := c.PollEvery
	if every == 0 {
		every = 3 * time.Second
	}
	for range pollTries {
		var status struct {
			StatusCode string `json:"status_code"`
		}
		err := c.call(ctx, http.MethodGet, container, token, url.Values{"fields": {"status_code"}}, &status)
		if err != nil {
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

// permalink reads where a published post can be seen. The post is out
// either way, so a failure only leaves the address empty.
func (c *Connector) permalink(ctx context.Context, id, token, field string) string {
	var out map[string]any
	if err := c.call(ctx, http.MethodGet, id, token, url.Values{"fields": {field}}, &out); err != nil {
		c.Log.WarnContext(ctx, "meta_permalink_unread", "err", err)
		return ""
	}
	link, _ := out[field].(string)
	return link
}

func (c *Connector) page(ctx context.Context) (connectedPage, error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return connectedPage{}, content.ErrNotConnected
	}
	if err != nil {
		return connectedPage{}, err
	}
	if row.Status != "connected" {
		return connectedPage{}, content.ErrNotConnected
	}
	token, err := c.Tokens.Open(row.Token)
	if err != nil {
		return connectedPage{}, err
	}
	return connectedPage{ID: row.AccountID.String, Token: string(token), InstagramID: row.InstagramID.String}, nil
}

// imageURLs are the public addresses of each image's largest web size.
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

// failed says why a Graph API call did not post. A refusal means nothing
// went out; a lost answer to the final, posting call may hide a post that
// did, so it is unknown_outcome and never retried on its own.
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
	case ge.Code == codeInvalidToken || ge.Code == codePermission || ge.Code >= 200 && ge.Code < 300:
		c.markError(ctx, "reconnect_required")
		return &content.ChannelError{Reason: "reconnect_required"}
	case slices.Contains(codesRateLimit, ge.Code):
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	return &content.ChannelError{Reason: "rejected"}
}

// markError flags the connection on the Connections page; a failure to record it is only logged.
func (c *Connector) markError(ctx context.Context, reason string) {
	err := db.New(c.Pool).SetConnectionError(ctx, db.SetConnectionErrorParams{
		Platform: platformName, LastError: text(reason), Now: c.Now(),
	})
	if err != nil {
		c.Log.WarnContext(ctx, "meta_error_unrecorded", "err", err)
	}
}
