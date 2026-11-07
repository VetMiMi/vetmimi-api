package linkedin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/media"
)

const maxImageBytes = 20 << 20

var (
	hashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
	// reserved are the characters LinkedIn's "little text" format gives a
	// meaning; unescaped, they can cut a post short.
	reserved = strings.NewReplacer(`\`, `\\`, `|`, `\|`, `{`, `\{`, `}`, `\}`, `@`, `\@`, `[`, `\[`, `]`, `\]`,
		`(`, `\(`, `)`, `\)`, `<`, `\<`, `>`, `\>`, `#`, `\#`, `*`, `\*`, `_`, `\_`, `~`, `\~`)
)

func (connector *Connector) Publish(ctx context.Context, version db.PostVersion) (content.Posted, error) {
	author, token, err := connector.connectedMember(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	body, err := connector.postBody(ctx, author, token, version)
	if err != nil {
		return content.Posted{}, err
	}
	req, err := connector.restRequest(ctx, http.MethodPost, "/rest/posts", token, body)
	if err != nil {
		return content.Posted{}, err
	}
	header, err := connector.do(req, nil)
	if err != nil {
		return content.Posted{}, connector.failed(ctx, err, true)
	}
	urn := header.Get("X-Restli-Id")
	posted := content.Posted{ExternalID: urn}
	if urn != "" {
		posted.Permalink = "https://www.linkedin.com/feed/update/" + urn + "/"
	}
	return posted, nil
}

// connectedMember reads the member's URN and token. An expired token needs
// Daw Mi to reconnect, so LinkedIn is not asked.
func (connector *Connector) connectedMember(ctx context.Context) (author, token string, err error) {
	row, err := db.New(connector.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", content.ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	if row.Status != "connected" {
		return "", "", content.ErrNotConnected
	}
	if expired(row.ExpiresAt, connector.Now()) {
		connector.markError(ctx, "reconnect_required")
		return "", "", &content.ChannelError{Reason: "reconnect_required"}
	}
	opened, err := connector.Tokens.Open(row.Token)
	if err != nil {
		return "", "", err
	}
	return "urn:li:person:" + row.AccountID.String, string(opened), nil
}

// postBody is the Posts API body for version: its text alone, with its link
// as an article, or with its one image. A post carries one kind of content,
// so with an image the link goes in the text.
func (connector *Connector) postBody(ctx context.Context, author, token string, version db.PostVersion) (map[string]any, error) {
	commentary := littleText(version.Text.String)
	body := map[string]any{
		"author":     author,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution":               "MAIN_FEED",
			"targetEntities":                 []any{},
			"thirdPartyDistributionChannels": []any{},
		},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	switch {
	case len(version.ImageIds) > 0:
		image, alt, err := connector.uploadImage(ctx, token, author, version)
		if err != nil {
			return nil, err
		}
		if version.LinkUrl.Valid {
			commentary += "\n\n" + littleText(version.LinkUrl.String)
		}
		attached := map[string]any{"id": image}
		if alt != "" {
			attached["altText"] = alt
		}
		body["content"] = map[string]any{"media": attached}
	case version.LinkUrl.Valid:
		// LinkedIn does not read the linked page, so the article takes the post's own title.
		post, err := db.New(connector.Pool).GetPost(ctx, version.PostID)
		if err != nil {
			return nil, err
		}
		body["content"] = map[string]any{"article": map[string]any{"source": version.LinkUrl.String, "title": post.Title}}
	}
	body["commentary"] = commentary
	return body, nil
}

// uploadImage copies the largest web size of version's image to LinkedIn and
// returns its image URN and English alt text.
func (connector *Connector) uploadImage(ctx context.Context, token, author string, version db.PostVersion) (urn, alt string, err error) {
	image, err := db.New(connector.Pool).GetMedia(ctx, version.ImageIds[0])
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", &content.ChannelError{Reason: "rejected"}
	}
	if err != nil {
		return "", "", err
	}
	var alts map[string]string
	_ = json.Unmarshal(image.Alt, &alts) // alt text is optional
	data, err := connector.download(ctx, media.Sizes(connector.MediaPublicURL, image.ID, image.Widths)[0].URL)
	if err != nil {
		return "", "", connector.failed(ctx, err, false)
	}
	uploadURL, urn, err := connector.initializeUpload(ctx, token, author)
	if err != nil {
		return "", "", err
	}
	err = connector.putImage(ctx, token, uploadURL, data)
	if err != nil {
		return "", "", err
	}
	return urn, alts["en"], nil
}

func (connector *Connector) initializeUpload(ctx context.Context, token, author string) (uploadURL, urn string, err error) {
	req, err := connector.restRequest(ctx, http.MethodPost, "/rest/images?action=initializeUpload", token,
		map[string]any{"initializeUploadRequest": map[string]string{"owner": author}})
	if err != nil {
		return "", "", err
	}
	var started struct {
		Value struct {
			UploadURL string `json:"uploadUrl"`
			Image     string `json:"image"`
		} `json:"value"`
	}
	_, err = connector.do(req, &started)
	if err != nil {
		return "", "", connector.failed(ctx, err, false)
	}
	return started.Value.UploadURL, started.Value.Image, nil
}

func (connector *Connector) putImage(ctx context.Context, token, uploadURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return connector.failed(ctx, err, false)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "image/jpeg")
	_, err = connector.do(req, nil)
	if err != nil {
		return connector.failed(ctx, err, false)
	}
	return nil
}

func (connector *Connector) download(ctx context.Context, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	res, err := connector.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("linkedin: read image: media answered %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, maxImageBytes))
}

// failed says why a LinkedIn call did not post. A refusal means nothing
// went out; a lost answer to the final, posting call may hide a post that
// did, so it is unknown_outcome and never retried on its own.
func (connector *Connector) failed(ctx context.Context, err error, final bool) error {
	var refused *apiError
	if !errors.As(err, &refused) || refused.Status/100 != 4 {
		connector.Log.WarnContext(ctx, "linkedin_unreachable", "err", err, "final", final)
		if final {
			return &content.ChannelError{Reason: "unknown_outcome"}
		}
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	connector.Log.WarnContext(ctx, "linkedin_refused", "status", refused.Status, "code", refused.Code)
	switch refused.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		connector.markError(ctx, "reconnect_required")
		return &content.ChannelError{Reason: "reconnect_required"}
	case http.StatusTooManyRequests:
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
		connector.Log.WarnContext(ctx, "linkedin_error_unrecorded", "err", err)
	}
}

// littleText escapes s for the commentary field, leaving hashtags as they
// are so LinkedIn links them.
func littleText(s string) string {
	var escaped strings.Builder
	last := 0
	for _, match := range hashtag.FindAllStringIndex(s, -1) {
		escaped.WriteString(reserved.Replace(s[last:match[0]]))
		escaped.WriteString(s[match[0]:match[1]])
		last = match[1]
	}
	escaped.WriteString(reserved.Replace(s[last:]))
	return escaped.String()
}
