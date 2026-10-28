package content

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type postURI struct {
	PostID string `uri:"postId" binding:"required,uuid"`
}

// id is safe to parse: BindURI has checked it is a uuid.
func (u postURI) id() pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(u.PostID), Valid: true}
}

type channelURI struct {
	PostID  string `uri:"postId" binding:"required,uuid"`
	Channel string `uri:"channel" binding:"required,oneof=facebook instagram linkedin"`
}

func (u channelURI) id() pgtype.UUID {
	return postURI{u.PostID}.id()
}

type postListQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=idea draft in_review approved scheduled publishing published archived"`
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
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

func (h *Handler) listPosts(c *gin.Context) {
	var q postListQuery
	if !web.BindQuery(c, &q) {
		return
	}
	f := PostFilter{Status: q.Status, Search: q.Search, Cursor: q.Cursor}
	if q.Limit != nil {
		f.Limit = *q.Limit
	}
	page, err := ListPosts(c.Request.Context(), db.New(h.Pool), f)
	if err != nil {
		web.Error(c, err)
		return
	}
	out := postListResponse{Items: make([]postSummaryResponse, len(page.Items)), NextCursor: page.NextCursor}
	for i, r := range page.Items {
		out.Items[i] = postSummaryView(r)
	}
	c.JSON(http.StatusOK, out)
}

func postSummaryView(r db.ListPostsRow) postSummaryResponse {
	v := postSummaryResponse{
		ID:           r.ID,
		Title:        r.Title,
		Kind:         r.Kind,
		Status:       r.Status,
		ScheduledAt:  utc(r.ScheduledAt),
		Channels:     append([]string{}, r.Channels...),
		Publications: make([]publicationSummaryResponse, len(r.PublicationChannels)),
		PublishedAt:  utc(r.PublishedAt),
		Version:      r.Version,
		CreatedAt:    r.CreatedAt.UTC(),
		UpdatedAt:    r.UpdatedAt.UTC(),
	}
	for i, ch := range r.PublicationChannels {
		v.Publications[i] = publicationSummaryResponse{Channel: ch, Status: r.PublicationStatuses[i]}
	}
	return v
}

type consentInput struct {
	Confirmed *bool  `json:"confirmed" binding:"required"`
	Note      string `json:"note" binding:"max=2000"`
}

type postCreateInput struct {
	Title    string         `json:"title" binding:"required,max=200"`
	Kind     string         `json:"kind" binding:"required,oneof=insight true_story announcement"`
	Status   string         `json:"status" binding:"omitempty,oneof=idea draft"`
	Consent  *consentInput  `json:"consent"`
	Versions *versionsInput `json:"versions"`
}

func (h *Handler) createPost(c *gin.Context) {
	var in postCreateInput
	if !web.BindJSON(c, &in) {
		return
	}
	e := Edit{Title: in.Title, Kind: in.Kind, Status: in.Status,
		Consent: consentOf(in.Consent), Versions: versionParams(in.Versions)}
	p, err := CreatePost(c.Request.Context(), h.Pool, e, actor(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusCreated, "post_created", p)
}

func (h *Handler) getPost(c *gin.Context) {
	var u postURI
	if !web.BindURI(c, &u) {
		return
	}
	p, err := GetPost(c.Request.Context(), db.New(h.Pool), u.id())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, postView(p))
}

// postPatchInput uses pointers so an empty title or kind is refused, not read as "unchanged".
type postPatchInput struct {
	Version  int32          `json:"version" binding:"required,min=1"`
	Title    *string        `json:"title" binding:"omitnil,min=1,max=200"`
	Kind     *string        `json:"kind" binding:"omitnil,oneof=insight true_story announcement"`
	Status   *string        `json:"status" binding:"omitnil,oneof=idea draft"`
	Consent  *consentInput  `json:"consent"`
	Versions *versionsInput `json:"versions"`
}

func (h *Handler) updatePost(c *gin.Context) {
	var u postURI
	var in postPatchInput
	if !web.BindURI(c, &u) || !web.BindJSON(c, &in) {
		return
	}
	e := Edit{Title: deref(in.Title), Kind: deref(in.Kind), Status: deref(in.Status),
		Consent: consentOf(in.Consent), Versions: versionParams(in.Versions)}
	p, err := UpdatePost(c.Request.Context(), h.Pool, u.id(), in.Version, e, actor(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	sendPost(c, http.StatusOK, "post_updated", p)
}

func (h *Handler) deletePost(c *gin.Context) {
	var u postURI
	if !web.BindURI(c, &u) {
		return
	}
	ctx := c.Request.Context()
	if err := DeletePost(ctx, db.New(h.Pool), u.id()); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "post_deleted", "request_id", web.RequestID(ctx), "post_id", u.PostID)
	c.Status(http.StatusNoContent)
}

// sendPost logs ids and status only: a post's text may tell a person's story.
func sendPost(c *gin.Context, status int, event string, p Post, attrs ...any) {
	ctx := c.Request.Context()
	slog.InfoContext(ctx, event, append([]any{"request_id", web.RequestID(ctx), "post_id", p.ID.String(),
		"status", p.Status, "version", p.Version}, attrs...)...)
	c.JSON(status, postView(p))
}

func actor(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}

func consentOf(in *consentInput) *Consent {
	if in == nil {
		return nil
	}
	return &Consent{Confirmed: *in.Confirmed, Note: in.Note}
}

type postResponse struct {
	ID           pgtype.UUID           `json:"id"`
	Title        string                `json:"title"`
	Kind         string                `json:"kind"`
	Status       string                `json:"status"`
	ScheduledAt  *time.Time            `json:"scheduledAt,omitempty"`
	Consent      consentResponse       `json:"consent"`
	ReviewNote   pgtype.Text           `json:"reviewNote,omitzero"`
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

func postView(p Post) postResponse {
	v := postResponse{
		ID:     p.ID,
		Title:  p.Title,
		Kind:   p.Kind,
		Status: p.Status,
		Consent: consentResponse{
			Confirmed:   p.ConsentConfirmedAt.Valid,
			Note:        p.ConsentNote,
			ConfirmedAt: utc(p.ConsentConfirmedAt),
			ConfirmedBy: p.ConsentConfirmedBy,
		},
		ScheduledAt:  utc(p.ScheduledAt),
		ReviewNote:   p.ReviewNote,
		AuthorID:     p.AuthorID,
		ApprovedAt:   utc(p.ApprovedAt),
		ApprovedBy:   p.ApprovedBy,
		PublishedAt:  utc(p.PublishedAt),
		Versions:     versionsView(p.Versions),
		Publications: make([]publicationResponse, len(p.Publications)),
		Version:      p.Version,
		CreatedAt:    p.CreatedAt.UTC(),
		UpdatedAt:    p.UpdatedAt.UTC(),
	}
	for i, r := range p.Publications {
		v.Publications[i] = publicationResponse{
			Channel:     r.Channel,
			Status:      r.Status,
			ExternalID:  r.ExternalID,
			Permalink:   r.Permalink,
			Error:       r.Error,
			Attempts:    r.Attempts,
			PublishedAt: utc(r.PublishedAt),
			UpdatedAt:   r.UpdatedAt.UTC(),
		}
	}
	return v
}

func utc(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
