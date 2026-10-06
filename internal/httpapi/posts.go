package httpapi

import (
	"cmp"
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// ListPosts lists posts newest first.
func (s *server) ListPosts(ctx context.Context, req gen.ListPostsRequestObject) (gen.ListPostsResponseObject, error) {
	p := req.Params
	page, err := content.ListPosts(ctx, db.New(s.Pool), content.PostFilter{
		Status: string(deref(p.Status)),
		Search: deref(p.Q),
		Cursor: deref(p.Cursor),
		Limit:  deref(p.Limit),
	})
	if err != nil {
		return nil, err
	}
	out := gen.ListPosts200JSONResponse{Items: make([]gen.PostSummary, len(page.Items)),
		NextCursor: nonEmpty(page.NextCursor)}
	for i, r := range page.Items {
		out.Items[i] = postSummaryView(r)
	}
	return out, nil
}

// CreatePost saves a new post by the signed-in author.
func (s *server) CreatePost(ctx context.Context, req gen.CreatePostRequestObject) (gen.CreatePostResponseObject, error) {
	b := req.Body
	e := content.Edit{Title: b.Title, Kind: string(b.Kind), Status: string(deref(b.Status)),
		Consent: consentInput(b.Consent), Versions: versionsInput(b.Versions)}
	p, err := content.CreatePost(ctx, s.Pool, e, actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_created", p)
	return gen.CreatePost201JSONResponse(postView(p)), nil
}

// GetPost reads one post with its versions and publications.
func (s *server) GetPost(ctx context.Context, req gen.GetPostRequestObject) (gen.GetPostResponseObject, error) {
	p, err := content.GetPost(ctx, db.New(s.Pool), uuid(req.PostId))
	if err != nil {
		return nil, err
	}
	return gen.GetPost200JSONResponse(postView(p)), nil
}

// UpdatePost saves an author's changes.
func (s *server) UpdatePost(ctx context.Context, req gen.UpdatePostRequestObject) (gen.UpdatePostResponseObject, error) {
	b := req.Body
	e := content.Edit{Title: deref(b.Title), Kind: string(deref(b.Kind)), Status: string(deref(b.Status)),
		Consent: consentInput(b.Consent), Versions: versionsInput(b.Versions)}
	p, err := content.UpdatePost(ctx, s.Pool, uuid(req.PostId), int32(b.Version), e, actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.RevalidateTasks(p)...)
	s.logPost(ctx, "post_updated", p)
	return gen.UpdatePost200JSONResponse(postView(p)), nil
}

// DeletePost deletes an idea or draft.
func (s *server) DeletePost(ctx context.Context, req gen.DeletePostRequestObject) (gen.DeletePostResponseObject, error) {
	if err := content.DeletePost(ctx, db.New(s.Pool), uuid(req.PostId)); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "post_deleted", "request_id", RequestID(ctx), "post_id", req.PostId.String())
	return gen.DeletePost204Response{}, nil
}

// SubmitPost puts a post in front of the reviewer.
func (s *server) SubmitPost(ctx context.Context, req gen.SubmitPostRequestObject) (gen.SubmitPostResponseObject, error) {
	p, err := content.Submit(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_submitted", p)
	return gen.SubmitPost200JSONResponse(postView(p)), nil
}

// RequestPostChanges sends a post back to its author.
func (s *server) RequestPostChanges(ctx context.Context, req gen.RequestPostChangesRequestObject) (gen.RequestPostChangesResponseObject, error) {
	p, err := content.RequestChanges(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), req.Body.Note, s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_changes_requested", p)
	return gen.RequestPostChanges200JSONResponse(postView(p)), nil
}

// ApprovePost approves a post whose channel versions are ready.
func (s *server) ApprovePost(ctx context.Context, req gen.ApprovePostRequestObject) (gen.ApprovePostResponseObject, error) {
	p, err := content.Approve(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_approved", p)
	return gen.ApprovePost200JSONResponse(postView(p)), nil
}

// SchedulePost sets when an approved post goes out.
func (s *server) SchedulePost(ctx context.Context, req gen.SchedulePostRequestObject) (gen.SchedulePostResponseObject, error) {
	p, err := content.Schedule(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), req.Body.ScheduledAt, s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.ScheduledTask(p)...)
	s.logPost(ctx, "post_scheduled", p)
	return gen.SchedulePost200JSONResponse(postView(p)), nil
}

// UnschedulePost cancels a post's schedule.
func (s *server) UnschedulePost(ctx context.Context, req gen.UnschedulePostRequestObject) (gen.UnschedulePostResponseObject, error) {
	p, err := content.Unschedule(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_unscheduled", p)
	return gen.UnschedulePost200JSONResponse(postView(p)), nil
}

// PublishPost publishes a post now.
func (s *server) PublishPost(ctx context.Context, req gen.PublishPostRequestObject) (gen.PublishPostResponseObject, error) {
	p, err := content.PublishNow(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, append(content.PublishTasks(p), content.RevalidateTasks(p)...)...)
	s.logPost(ctx, "post_publishing", p)
	return gen.PublishPost200JSONResponse(postView(p)), nil
}

// ArchivePost takes a post out of the workflow.
func (s *server) ArchivePost(ctx context.Context, req gen.ArchivePostRequestObject) (gen.ArchivePostResponseObject, error) {
	p, err := content.Archive(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.RevalidateTasks(p)...)
	s.logPost(ctx, "post_archived", p)
	return gen.ArchivePost200JSONResponse(postView(p)), nil
}

// MarkPostChannelPosted records a channel Daw Mi posted by hand.
func (s *server) MarkPostChannelPosted(ctx context.Context, req gen.MarkPostChannelPostedRequestObject) (gen.MarkPostChannelPostedResponseObject, error) {
	p, err := content.MarkPosted(ctx, s.Pool, uuid(req.PostId), string(req.Channel), deref(req.Body.Permalink), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_channel_marked_posted", p, "channel", string(req.Channel))
	return gen.MarkPostChannelPosted200JSONResponse(postView(p)), nil
}

// RetryPostChannel gives a failed social channel another round of attempts.
func (s *server) RetryPostChannel(ctx context.Context, req gen.RetryPostChannelRequestObject) (gen.RetryPostChannelResponseObject, error) {
	p, err := content.Retry(ctx, s.Pool, uuid(req.PostId), string(req.Channel), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.PublishTasks(p)...)
	s.logPost(ctx, "post_channel_retried", p, "channel", string(req.Channel))
	return gen.RetryPostChannel200JSONResponse(postView(p)), nil
}

// enqueue queues tasks once the change that led to them has committed.
func (s *server) enqueue(ctx context.Context, tasks ...platform.Task) {
	if s.Queue != nil {
		s.Queue.Enqueue(ctx, tasks...)
	}
}

func (s *server) logPost(ctx context.Context, msg string, p content.Post, attrs ...any) {
	s.Log.InfoContext(ctx, msg, append([]any{"request_id", RequestID(ctx), "post_id", p.ID.String(),
		"status", p.Status, "version", p.Version}, attrs...)...)
}

// ListPublicArticles lists the published articles in the visitor's locale.
func (s *server) ListPublicArticles(ctx context.Context, req gen.ListPublicArticlesRequestObject) (gen.ListPublicArticlesResponseObject, error) {
	p := req.Params
	locale := cmp.Or(deref(p.Locale), gen.En)
	articles, err := content.ListArticles(ctx, db.New(s.Pool), string(locale), string(deref(p.Kind)),
		cmp.Or(deref(p.Limit), 100))
	if err != nil {
		return nil, err
	}
	out := gen.ListPublicArticles200JSONResponse{Locale: locale, Items: make([]gen.PublicArticleSummary, len(articles))}
	for i, a := range articles {
		out.Items[i] = gen.PublicArticleSummary{Slug: a.Slug, Kind: gen.PostKind(a.Kind), Title: a.Title,
			Excerpt: a.Excerpt, CoverImage: s.coverView(a.Cover), PublishedAt: a.PublishedAt.UTC()}
	}
	return out, nil
}

// GetPublicArticle reads one published article in the visitor's locale.
func (s *server) GetPublicArticle(ctx context.Context, req gen.GetPublicArticleRequestObject) (gen.GetPublicArticleResponseObject, error) {
	locale := cmp.Or(deref(req.Params.Locale), gen.En)
	a, err := content.GetArticle(ctx, db.New(s.Pool), req.Slug, string(locale))
	if err != nil {
		return nil, err
	}
	return gen.GetPublicArticle200JSONResponse{Slug: a.Slug, Kind: gen.PostKind(a.Kind), Title: a.Title,
		Excerpt: a.Excerpt, Body: a.Body, SeoTitle: a.SEOTitle, SeoDescription: a.SEODescription,
		CoverImage: s.coverView(a.Cover), PublishedAt: a.PublishedAt.UTC()}, nil
}

func (s *server) coverView(c *content.Cover) *gen.PublicImage {
	if c == nil {
		return nil
	}
	return &gen.PublicImage{Id: openapi_types.UUID(c.ID.Bytes), Alt: c.Alt, Width: int(c.Width),
		Height: int(c.Height), Sizes: s.imageSizes(c.ID, c.Widths)}
}
