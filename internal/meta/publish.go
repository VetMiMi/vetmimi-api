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

// pageAccess is what posting to the connected Page needs.
type pageAccess struct {
	ID          string
	Token       string
	InstagramID string
}

// pollTries keeps the wait to about a minute, inside the content scheduler's stuck limit.
const pollTries = 20

// Graph API error codes. Codes 200 to 299 are missing permissions too.
const (
	codeInvalidToken = 190
	codePermission   = 10
)

var codesRateLimit = []int{4, 17, 32, 613, 80001, 80002}

// PublishFacebook posts version to the Page: its text with its link, or with
// its images, which are uploaded unpublished first.
func (connector *Connector) PublishFacebook(ctx context.Context, version db.PostVersion) (content.Posted, error) {
	page, err := connector.connectedPage(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	images, err := connector.imageURLs(ctx, version.ImageIds)
	if err != nil {
		return content.Posted{}, err
	}
	post := url.Values{"message": {version.Text.String}}
	if len(images) == 0 && version.LinkUrl.Valid {
		post.Set("link", version.LinkUrl.String)
	} else if version.LinkUrl.Valid {
		// A post with photos has no link preview, so the link goes in the text.
		post.Set("message", version.Text.String+"\n\n"+version.LinkUrl.String)
	}
	for i, image := range images {
		photoID, err := connector.uploadPhoto(ctx, page, image)
		if err != nil {
			return content.Posted{}, err
		}
		attached, _ := json.Marshal(map[string]string{"media_fbid": photoID}) // a string map always marshals
		post.Set(fmt.Sprintf("attached_media[%d]", i), string(attached))
	}
	var created struct {
		ID string `json:"id"`
	}
	err = connector.call(ctx, http.MethodPost, page.ID+"/feed", page.Token, post, &created)
	if err != nil {
		return content.Posted{}, connector.failed(ctx, err, true)
	}
	return content.Posted{
		ExternalID: created.ID,
		Permalink:  connector.permalink(ctx, created.ID, page.Token, "permalink_url"),
	}, nil
}

// PublishInstagram posts version to the Instagram account linked to the
// Page: one image, or a carousel of up to ten. Instagram must finish
// processing the container before it can be published.
func (connector *Connector) PublishInstagram(ctx context.Context, version db.PostVersion) (content.Posted, error) {
	page, err := connector.connectedPage(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	if page.InstagramID == "" {
		return content.Posted{}, content.ErrNotConnected
	}
	images, err := connector.imageURLs(ctx, version.ImageIds)
	if err != nil {
		return content.Posted{}, err
	}
	container, err := connector.instagramContainer(ctx, page, images, version.Text.String)
	if err != nil {
		return content.Posted{}, err
	}
	err = connector.waitFinished(ctx, container, page.Token)
	if err != nil {
		return content.Posted{}, err
	}
	var published struct {
		ID string `json:"id"`
	}
	err = connector.call(ctx, http.MethodPost, page.InstagramID+"/media_publish", page.Token,
		url.Values{"creation_id": {container}}, &published)
	if err != nil {
		return content.Posted{}, connector.failed(ctx, err, true)
	}
	return content.Posted{
		ExternalID: published.ID,
		Permalink:  connector.permalink(ctx, published.ID, page.Token, "permalink"),
	}, nil
}

// connectedPage reads the Page's id, token and Instagram account.
func (connector *Connector) connectedPage(ctx context.Context) (pageAccess, error) {
	row, err := db.New(connector.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return pageAccess{}, content.ErrNotConnected
	}
	if err != nil {
		return pageAccess{}, err
	}
	if row.Status != "connected" {
		return pageAccess{}, content.ErrNotConnected
	}
	token, err := connector.Tokens.Open(row.Token)
	if err != nil {
		return pageAccess{}, err
	}
	return pageAccess{
		ID:          row.AccountID.String,
		Token:       string(token),
		InstagramID: row.InstagramID.String,
	}, nil
}

// imageURLs are the public addresses of each image's largest web size.
func (connector *Connector) imageURLs(ctx context.Context, ids []pgtype.UUID) ([]string, error) {
	urls := make([]string, len(ids))
	for i, id := range ids {
		image, err := db.New(connector.Pool).GetMedia(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &content.ChannelError{Reason: "rejected"}
		}
		if err != nil {
			return nil, err
		}
		urls[i] = media.Sizes(connector.MediaPublicURL, image.ID, image.Widths)[0].URL
	}
	return urls, nil
}

func (connector *Connector) uploadPhoto(ctx context.Context, page pageAccess, imageURL string) (string, error) {
	var photo struct {
		ID string `json:"id"`
	}
	err := connector.call(ctx, http.MethodPost, page.ID+"/photos", page.Token,
		url.Values{"url": {imageURL}, "published": {"false"}}, &photo)
	if err != nil {
		return "", connector.failed(ctx, err, false)
	}
	return photo.ID, nil
}

// instagramContainer creates the media container for one image, or a
// carousel container over one child container per image.
func (connector *Connector) instagramContainer(ctx context.Context, page pageAccess, images []string, caption string) (string, error) {
	if len(images) == 1 {
		return connector.container(ctx, page, url.Values{"image_url": {images[0]}, "caption": {caption}})
	}
	children := make([]string, len(images))
	for i, image := range images {
		child, err := connector.container(ctx, page, url.Values{"image_url": {image}, "is_carousel_item": {"true"}})
		if err != nil {
			return "", err
		}
		children[i] = child
	}
	return connector.container(ctx, page, url.Values{
		"media_type": {"CAROUSEL"},
		"children":   {strings.Join(children, ",")},
		"caption":    {caption},
	})
}

func (connector *Connector) container(ctx context.Context, page pageAccess, params url.Values) (string, error) {
	var created struct {
		ID string `json:"id"`
	}
	err := connector.call(ctx, http.MethodPost, page.InstagramID+"/media", page.Token, params, &created)
	if err != nil {
		return "", connector.failed(ctx, err, false)
	}
	return created.ID, nil
}

// waitFinished polls container until Instagram has processed its images.
// One it could not process is rejected for good; one still in progress
// after pollTries is tried again later, with new containers.
func (connector *Connector) waitFinished(ctx context.Context, container, token string) error {
	every := connector.PollEvery
	if every == 0 {
		every = 3 * time.Second
	}
	for range pollTries {
		var status struct {
			StatusCode string `json:"status_code"`
		}
		err := connector.call(ctx, http.MethodGet, container, token, url.Values{"fields": {"status_code"}}, &status)
		if err != nil {
			return connector.failed(ctx, err, false)
		}
		switch status.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR", "EXPIRED":
			connector.Log.WarnContext(ctx, "meta_container_failed", "status", status.StatusCode)
			return &content.ChannelError{Reason: "rejected"}
		}
		select {
		case <-ctx.Done():
			return connector.failed(ctx, ctx.Err(), false)
		case <-time.After(every):
		}
	}
	return &content.ChannelError{Reason: "connection_failed", Transient: true}
}

// permalink reads where a published post can be seen. The post is out
// either way, so a failure only leaves the address empty.
func (connector *Connector) permalink(ctx context.Context, id, token, field string) string {
	var fields map[string]any
	err := connector.call(ctx, http.MethodGet, id, token, url.Values{"fields": {field}}, &fields)
	if err != nil {
		connector.Log.WarnContext(ctx, "meta_permalink_unread", "err", err)
		return ""
	}
	link, _ := fields[field].(string) // a missing or non-string field leaves the address empty
	return link
}

// failed says why a Graph API call did not post. A refusal means nothing
// went out; a lost answer to the final, posting call may hide a post that
// did, so it is unknown_outcome and never retried on its own.
func (connector *Connector) failed(ctx context.Context, err error, final bool) error {
	var refused *apiError
	if !errors.As(err, &refused) || refused.Status/100 != 4 {
		connector.Log.WarnContext(ctx, "meta_unreachable", "err", err, "final", final)
		if final {
			return &content.ChannelError{Reason: "unknown_outcome"}
		}
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	connector.Log.WarnContext(ctx, "meta_refused", "code", refused.Code, "status", refused.Status, "message", refused.Message)
	switch {
	case refused.Code == codeInvalidToken || refused.Code == codePermission || refused.Code >= 200 && refused.Code < 300:
		connector.markError(ctx, "reconnect_required")
		return &content.ChannelError{Reason: "reconnect_required"}
	case slices.Contains(codesRateLimit, refused.Code):
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	return &content.ChannelError{Reason: "rejected"}
}

// markError flags the connection on the Connections page; a failure to record it is only logged.
func (connector *Connector) markError(ctx context.Context, reason string) {
	err := db.New(connector.Pool).SetConnectionError(ctx, db.SetConnectionErrorParams{
		Platform:  platformName,
		LastError: text(reason),
		Now:       connector.Now(),
	})
	if err != nil {
		connector.Log.WarnContext(ctx, "meta_error_unrecorded", "err", err)
	}
}
