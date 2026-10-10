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

func (c *Connector) Publish(ctx context.Context, v db.PostVersion) (content.Posted, error) {
	author, token, err := c.connectedMember(ctx)
	if err != nil {
		return content.Posted{}, err
	}
	post, err := c.post(ctx, author, token, v)
	if err != nil {
		return content.Posted{}, err
	}
	req, err := c.rest(ctx, http.MethodPost, "/rest/posts", token, post)
	if err != nil {
		return content.Posted{}, err
	}
	header, err := c.do(req, nil)
	if err != nil {
		return content.Posted{}, c.failed(ctx, err, true)
	}
	urn := header.Get("X-Restli-Id")
	out := content.Posted{ExternalID: urn}
	if urn != "" {
		out.Permalink = "https://www.linkedin.com/feed/update/" + urn + "/"
	}
	return out, nil
}

// post is the Posts API body for v: its text alone, with its link as an
// article, or with its one image. A post carries one kind of content, so
// with an image the link goes in the text.
func (c *Connector) post(ctx context.Context, author, token string, v db.PostVersion) (map[string]any, error) {
	commentary := littleText(v.Text.String)
	post := map[string]any{
		"author":     author,
		"visibility": "PUBLIC",
		"distribution": map[string]any{
			"feedDistribution": "MAIN_FEED", "targetEntities": []any{}, "thirdPartyDistributionChannels": []any{},
		},
		"lifecycleState":            "PUBLISHED",
		"isReshareDisabledByAuthor": false,
	}
	switch {
	case len(v.ImageIds) > 0:
		image, alt, err := c.uploadImage(ctx, token, author, v)
		if err != nil {
			return nil, err
		}
		if v.LinkUrl.Valid {
			commentary += "\n\n" + littleText(v.LinkUrl.String)
		}
		attached := map[string]any{"id": image}
		if alt != "" {
			attached["altText"] = alt
		}
		post["content"] = map[string]any{"media": attached}
	case v.LinkUrl.Valid:
		// LinkedIn does not read the linked page, so the article takes the post's own title.
		p, err := db.New(c.Pool).GetPost(ctx, v.PostID)
		if err != nil {
			return nil, err
		}
		post["content"] = map[string]any{"article": map[string]any{"source": v.LinkUrl.String, "title": p.Title}}
	}
	post["commentary"] = commentary
	return post, nil
}

// connectedMember reads the member's URN and token. An expired token needs
// Daw Mi to reconnect, so LinkedIn is not asked.
func (c *Connector) connectedMember(ctx context.Context) (author, token string, err error) {
	row, err := db.New(c.Pool).GetConnection(ctx, platformName)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", content.ErrNotConnected
	}
	if err != nil {
		return "", "", err
	}
	if row.Status != "connected" {
		return "", "", content.ErrNotConnected
	}
	if expired(row.ExpiresAt, c.Now()) {
		c.markError(ctx, "reconnect_required")
		return "", "", &content.ChannelError{Reason: "reconnect_required"}
	}
	opened, err := c.Tokens.Open(row.Token)
	if err != nil {
		return "", "", err
	}
	return "urn:li:person:" + row.AccountID.String, string(opened), nil
}

// uploadImage copies the largest web size of v's image to LinkedIn and
// returns its image URN and English alt text.
func (c *Connector) uploadImage(ctx context.Context, token, author string, v db.PostVersion) (urn, alt string, err error) {
	m, err := db.New(c.Pool).GetMedia(ctx, v.ImageIds[0])
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", &content.ChannelError{Reason: "rejected"}
	}
	if err != nil {
		return "", "", err
	}
	var alts map[string]string
	_ = json.Unmarshal(m.Alt, &alts) // alt text is optional
	data, err := c.download(ctx, media.Sizes(c.MediaPublicURL, m.ID, m.Widths)[0].URL)
	if err != nil {
		return "", "", c.failed(ctx, err, false)
	}
	uploadURL, urn, err := c.initializeUpload(ctx, token, author)
	if err != nil {
		return "", "", err
	}
	if err := c.putImage(ctx, token, uploadURL, data); err != nil {
		return "", "", err
	}
	return urn, alts["en"], nil
}

func (c *Connector) initializeUpload(ctx context.Context, token, author string) (uploadURL, urn string, err error) {
	req, err := c.rest(ctx, http.MethodPost, "/rest/images?action=initializeUpload", token,
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
	if _, err := c.do(req, &started); err != nil {
		return "", "", c.failed(ctx, err, false)
	}
	return started.Value.UploadURL, started.Value.Image, nil
}

func (c *Connector) putImage(ctx context.Context, token, uploadURL string, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return c.failed(ctx, err, false)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "image/jpeg")
	if _, err := c.do(req, nil); err != nil {
		return c.failed(ctx, err, false)
	}
	return nil
}

func (c *Connector) download(ctx context.Context, link string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	res, err := c.httpClient().Do(req)
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
func (c *Connector) failed(ctx context.Context, err error, final bool) error {
	var ae *apiError
	if !errors.As(err, &ae) || ae.Status/100 != 4 {
		c.Log.WarnContext(ctx, "linkedin_unreachable", "err", err, "final", final)
		if final {
			return &content.ChannelError{Reason: "unknown_outcome"}
		}
		return &content.ChannelError{Reason: "connection_failed", Transient: true}
	}
	c.Log.WarnContext(ctx, "linkedin_refused", "status", ae.Status, "code", ae.Code)
	switch ae.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		c.markError(ctx, "reconnect_required")
		return &content.ChannelError{Reason: "reconnect_required"}
	case http.StatusTooManyRequests:
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
		c.Log.WarnContext(ctx, "linkedin_error_unrecorded", "err", err)
	}
}

var (
	hashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
	// reserved are the characters LinkedIn's "little text" format gives a
	// meaning; unescaped, they can cut a post short.
	reserved = strings.NewReplacer(`\`, `\\`, `|`, `\|`, `{`, `\{`, `}`, `\}`, `@`, `\@`, `[`, `\[`, `]`, `\]`,
		`(`, `\(`, `)`, `\)`, `<`, `\<`, `>`, `\>`, `#`, `\#`, `*`, `\*`, `_`, `\_`, `~`, `\~`)
)

// littleText escapes s for the commentary field, leaving hashtags as they
// are so LinkedIn links them.
func littleText(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range hashtag.FindAllStringIndex(s, -1) {
		b.WriteString(reserved.Replace(s[last:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		last = m[1]
	}
	b.WriteString(reserved.Replace(s[last:]))
	return b.String()
}
