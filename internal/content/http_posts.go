package content

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listPostsQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=idea draft in_review approved scheduled publishing published archived"`
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
}

type createPostBody struct {
	Title    string        `json:"title" binding:"required,max=200"`
	Kind     string        `json:"kind" binding:"required,oneof=insight true_story announcement"`
	Status   string        `json:"status" binding:"omitempty,oneof=idea draft"`
	Consent  *consentBody  `json:"consent"`
	Versions *versionsBody `json:"versions"`
}

// updatePostBody uses pointers so an empty title or kind is refused, not read as "unchanged".
type updatePostBody struct {
	Version  int32         `json:"version" binding:"required,min=1"`
	Title    *string       `json:"title" binding:"omitnil,min=1,max=200"`
	Kind     *string       `json:"kind" binding:"omitnil,oneof=insight true_story announcement"`
	Status   *string       `json:"status" binding:"omitnil,oneof=idea draft"`
	Consent  *consentBody  `json:"consent"`
	Versions *versionsBody `json:"versions"`
}

type consentBody struct {
	Confirmed *bool  `json:"confirmed" binding:"required"`
	Note      string `json:"note" binding:"max=2000"`
}

type postListResponse struct {
	Items      []postSummaryResponse `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
}

type postSummaryResponse struct {
	ID           pgtype.UUID                  `json:"id"`
	Title        string                       `json:"title"`
	Kind         string                       `json:"kind"`
	Status       string                       `json:"status"`
	ScheduledAt  *time.Time                   `json:"scheduledAt,omitempty"`
	Channels     []string                     `json:"channels"`
	Publications []publicationSummaryResponse `json:"publications"`
	PublishedAt  *time.Time                   `json:"publishedAt,omitempty"`
	Version      int32                        `json:"version"`
	CreatedAt    time.Time                    `json:"createdAt"`
	UpdatedAt    time.Time                    `json:"updatedAt"`
}

type publicationSummaryResponse struct {
	Channel string `json:"channel"`
	Status  string `json:"status"`
}

type postResponse struct {
	ID           pgtype.UUID           `json:"id"`
	Title        string                `json:"title"`
	Kind         string                `json:"kind"`
	Status       string                `json:"status"`
	ScheduledAt  *time.Time            `json:"scheduledAt,omitempty"`
	Consent      consentResponse       `json:"consent"`
	AuthorID     pgtype.UUID           `json:"authorId,omitzero"`
	ApprovedAt   *time.Time            `json:"approvedAt,omitempty"`
	ApprovedBy   pgtype.UUID           `json:"approvedBy,omitzero"`
	PublishedAt  *time.Time            `json:"publishedAt,omitempty"`
	Versions     versionsResponse      `json:"versions"`
	Publications []publicationResponse `json:"publications"`
	Version      int32                 `json:"version"`
	CreatedAt    time.Time             `json:"createdAt"`
	UpdatedAt    time.Time             `json:"updatedAt"`
}

type consentResponse struct {
	Confirmed   bool        `json:"confirmed"`
	Note        pgtype.Text `json:"note,omitzero"`
	ConfirmedAt *time.Time  `json:"confirmedAt,omitempty"`
	ConfirmedBy pgtype.UUID `json:"confirmedBy,omitzero"`
}

type publicationResponse struct {
	Channel     string      `json:"channel"`
	Status      string      `json:"status"`
	ExternalID  pgtype.Text `json:"externalId,omitzero"`
	Permalink   pgtype.Text `json:"permalink,omitzero"`
	Error       pgtype.Text `json:"error,omitzero"`
	Attempts    int32       `json:"attempts"`
	PublishedAt *time.Time  `json:"publishedAt,omitempty"`
	UpdatedAt   time.Time   `json:"updatedAt"`
}

func (h *Handler) listPosts(c *gin.Context) {
	var query listPostsQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	filter := PostFilter{
		Status: query.Status,
		Search: query.Search,
		Cursor: query.Cursor,
	}
	if query.Limit != nil {
		filter.Limit = *query.Limit
	}
	page, err := ListPosts(ctx, db.New(h.Pool), filter)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := postListResponse{
		Items:      make([]postSummaryResponse, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for i, row := range page.Items {
		response.Items[i] = newPostSummaryResponse(row)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) createPost(c *gin.Context) {
	var body createPostBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	edit := Edit{
		Title:    body.Title,
		Kind:     body.Kind,
		Status:   body.Status,
		Consent:  newConsent(body.Consent),
		Versions: newVersionParams(body.Versions),
	}
	post, err := CreatePost(ctx, h.Pool, edit, currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logPost(ctx, "post_created", post)
	c.JSON(http.StatusCreated, newPostResponse(post))
}

func (h *Handler) getPost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	post, err := GetPost(ctx, db.New(h.Pool), postID)
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newPostResponse(post))
}

func (h *Handler) updatePost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	var body updatePostBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	edit := Edit{
		Title:    valueOrEmpty(body.Title),
		Kind:     valueOrEmpty(body.Kind),
		Status:   valueOrEmpty(body.Status),
		Consent:  newConsent(body.Consent),
		Versions: newVersionParams(body.Versions),
	}
	post, err := UpdatePost(ctx, h.Pool, postID, body.Version, edit, currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logPost(ctx, "post_updated", post)
	c.JSON(http.StatusOK, newPostResponse(post))
}

func (h *Handler) deletePost(c *gin.Context) {
	postID, ok := web.PathUUID(c, "postId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := DeletePost(ctx, db.New(h.Pool), postID)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "post_deleted", "request_id", web.RequestID(ctx), "post_id", postID.String())
	c.Status(http.StatusNoContent)
}

func newPostSummaryResponse(row db.ListPostsRow) postSummaryResponse {
	response := postSummaryResponse{
		ID:           row.ID,
		Title:        row.Title,
		Kind:         row.Kind,
		Status:       row.Status,
		ScheduledAt:  utcOrNil(row.ScheduledAt),
		Channels:     append([]string{}, row.Channels...),
		Publications: make([]publicationSummaryResponse, len(row.PublicationChannels)),
		PublishedAt:  utcOrNil(row.PublishedAt),
		Version:      row.Version,
		CreatedAt:    row.CreatedAt.UTC(),
		UpdatedAt:    row.UpdatedAt.UTC(),
	}
	for i, channel := range row.PublicationChannels {
		response.Publications[i] = publicationSummaryResponse{
			Channel: channel,
			Status:  row.PublicationStatuses[i],
		}
	}
	return response
}

func newPostResponse(post Post) postResponse {
	response := postResponse{
		ID:     post.ID,
		Title:  post.Title,
		Kind:   post.Kind,
		Status: post.Status,
		Consent: consentResponse{
			Confirmed:   post.ConsentConfirmedAt.Valid,
			Note:        post.ConsentNote,
			ConfirmedAt: utcOrNil(post.ConsentConfirmedAt),
			ConfirmedBy: post.ConsentConfirmedBy,
		},
		ScheduledAt:  utcOrNil(post.ScheduledAt),
		AuthorID:     post.AuthorID,
		ApprovedAt:   utcOrNil(post.ApprovedAt),
		ApprovedBy:   post.ApprovedBy,
		PublishedAt:  utcOrNil(post.PublishedAt),
		Versions:     newVersionsResponse(post.Versions),
		Publications: make([]publicationResponse, len(post.Publications)),
		Version:      post.Version,
		CreatedAt:    post.CreatedAt.UTC(),
		UpdatedAt:    post.UpdatedAt.UTC(),
	}
	for i, publication := range post.Publications {
		response.Publications[i] = publicationResponse{
			Channel:     publication.Channel,
			Status:      publication.Status,
			ExternalID:  publication.ExternalID,
			Permalink:   publication.Permalink,
			Error:       publication.Error,
			Attempts:    publication.Attempts,
			PublishedAt: utcOrNil(publication.PublishedAt),
			UpdatedAt:   publication.UpdatedAt.UTC(),
		}
	}
	return response
}

func newConsent(body *consentBody) *Consent {
	if body == nil {
		return nil
	}
	return &Consent{Confirmed: *body.Confirmed, Note: body.Note}
}

// logPost logs ids and status only: a post's text may tell a person's story.
func logPost(ctx context.Context, event string, post Post, attrs ...any) {
	attrs = append(attrs,
		"request_id", web.RequestID(ctx),
		"post_id", post.ID.String(),
		"status", post.Status,
		"version", post.Version,
	)
	slog.InfoContext(ctx, event, attrs...)
}

// currentUserID is safe on admin routes: web.SignedIn has already found the session.
func currentUserID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}

func utcOrNil(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	utc := t.Time.UTC()
	return &utc
}

func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
