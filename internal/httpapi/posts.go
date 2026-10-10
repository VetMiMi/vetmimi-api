package httpapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

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

func (s *server) GetPost(ctx context.Context, req gen.GetPostRequestObject) (gen.GetPostResponseObject, error) {
	p, err := content.GetPost(ctx, db.New(s.Pool), uuid(req.PostId))
	if err != nil {
		return nil, err
	}
	return gen.GetPost200JSONResponse(postView(p)), nil
}

func (s *server) UpdatePost(ctx context.Context, req gen.UpdatePostRequestObject) (gen.UpdatePostResponseObject, error) {
	b := req.Body
	e := content.Edit{Title: deref(b.Title), Kind: string(deref(b.Kind)), Status: string(deref(b.Status)),
		Consent: consentInput(b.Consent), Versions: versionsInput(b.Versions)}
	p, err := content.UpdatePost(ctx, s.Pool, uuid(req.PostId), int32(b.Version), e, actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_updated", p)
	return gen.UpdatePost200JSONResponse(postView(p)), nil
}

func (s *server) DeletePost(ctx context.Context, req gen.DeletePostRequestObject) (gen.DeletePostResponseObject, error) {
	if err := content.DeletePost(ctx, db.New(s.Pool), uuid(req.PostId)); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "post_deleted", "request_id", RequestID(ctx), "post_id", req.PostId.String())
	return gen.DeletePost204Response{}, nil
}

func (s *server) SubmitPost(ctx context.Context, req gen.SubmitPostRequestObject) (gen.SubmitPostResponseObject, error) {
	p, err := content.Submit(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_submitted", p)
	return gen.SubmitPost200JSONResponse(postView(p)), nil
}

func (s *server) RequestPostChanges(ctx context.Context, req gen.RequestPostChangesRequestObject) (gen.RequestPostChangesResponseObject, error) {
	p, err := content.RequestChanges(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), req.Body.Note, s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_changes_requested", p)
	return gen.RequestPostChanges200JSONResponse(postView(p)), nil
}

func (s *server) ApprovePost(ctx context.Context, req gen.ApprovePostRequestObject) (gen.ApprovePostResponseObject, error) {
	p, err := content.Approve(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_approved", p)
	return gen.ApprovePost200JSONResponse(postView(p)), nil
}

func (s *server) SchedulePost(ctx context.Context, req gen.SchedulePostRequestObject) (gen.SchedulePostResponseObject, error) {
	p, err := content.Schedule(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), req.Body.ScheduledAt, s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.ScheduledTask(p)...)
	s.logPost(ctx, "post_scheduled", p)
	return gen.SchedulePost200JSONResponse(postView(p)), nil
}

func (s *server) UnschedulePost(ctx context.Context, req gen.UnschedulePostRequestObject) (gen.UnschedulePostResponseObject, error) {
	p, err := content.Unschedule(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_unscheduled", p)
	return gen.UnschedulePost200JSONResponse(postView(p)), nil
}

func (s *server) PublishPost(ctx context.Context, req gen.PublishPostRequestObject) (gen.PublishPostResponseObject, error) {
	p, err := content.PublishNow(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, append(content.PublishTasks(p), content.RevalidateTasks(p)...)...)
	s.logPost(ctx, "post_publishing", p)
	return gen.PublishPost200JSONResponse(postView(p)), nil
}

func (s *server) ArchivePost(ctx context.Context, req gen.ArchivePostRequestObject) (gen.ArchivePostResponseObject, error) {
	p, err := content.Archive(ctx, s.Pool, uuid(req.PostId), int32(req.Body.Version), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.RevalidateTasks(p)...)
	s.logPost(ctx, "post_archived", p)
	return gen.ArchivePost200JSONResponse(postView(p)), nil
}

func (s *server) MarkPostChannelPosted(ctx context.Context, req gen.MarkPostChannelPostedRequestObject) (gen.MarkPostChannelPostedResponseObject, error) {
	p, err := content.MarkPosted(ctx, s.Pool, uuid(req.PostId), string(req.Channel), deref(req.Body.Permalink), s.Now())
	if err != nil {
		return nil, err
	}
	s.logPost(ctx, "post_channel_marked_posted", p, "channel", string(req.Channel))
	return gen.MarkPostChannelPosted200JSONResponse(postView(p)), nil
}

func (s *server) RetryPostChannel(ctx context.Context, req gen.RetryPostChannelRequestObject) (gen.RetryPostChannelResponseObject, error) {
	p, err := content.Retry(ctx, s.Pool, uuid(req.PostId), string(req.Channel), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, content.PublishTasks(p)...)
	s.logPost(ctx, "post_channel_retried", p, "channel", string(req.Channel))
	return gen.RetryPostChannel200JSONResponse(postView(p)), nil
}

func (s *server) logPost(ctx context.Context, msg string, p content.Post, attrs ...any) {
	s.Log.InfoContext(ctx, msg, append([]any{"request_id", RequestID(ctx), "post_id", p.ID.String(),
		"status", p.Status, "version", p.Version}, attrs...)...)
}

func consentInput(c *gen.ConsentInput) *content.Consent {
	if c == nil {
		return nil
	}
	return &content.Consent{Confirmed: c.Confirmed, Note: deref(c.Note)}
}

// versionsInput is one row per channel the request names; an Instagram
// caption is stored as its text.
func versionsInput(v *gen.PostVersions) []db.SavePostVersionParams {
	if v == nil {
		return nil
	}
	var out []db.SavePostVersionParams
	if w := v.Website; w != nil {
		out = append(out, db.SavePostVersionParams{Channel: "website", Enabled: w.Enabled,
			Slug: optionalText(w.Slug), Title: localizedJSON(w.Title), Excerpt: localizedJSON(w.Excerpt),
			Body: localizedJSON((*gen.LocalizedText)(w.Body)), CoverImageID: optionalUUID(w.CoverImageId),
			SeoTitle: localizedJSON(w.SeoTitle), SeoDescription: localizedJSON(w.SeoDescription)})
	}
	if f := v.Facebook; f != nil {
		out = append(out, db.SavePostVersionParams{Channel: "facebook", Enabled: f.Enabled,
			Text: optionalText(f.Text), LinkUrl: optionalText(f.Link), ImageIds: uuids(f.ImageIds)})
	}
	if i := v.Instagram; i != nil {
		out = append(out, db.SavePostVersionParams{Channel: "instagram", Enabled: i.Enabled,
			Text: optionalText(i.Caption), ImageIds: uuids(i.ImageIds)})
	}
	if l := v.Linkedin; l != nil {
		out = append(out, db.SavePostVersionParams{Channel: "linkedin", Enabled: l.Enabled,
			Text: optionalText(l.Text), LinkUrl: optionalText(l.Link), ImageIds: uuids(l.ImageIds)})
	}
	return out
}

func postView(p content.Post) gen.Post {
	v := gen.Post{
		Id:     openapi_types.UUID(p.ID.Bytes),
		Title:  p.Title,
		Kind:   gen.PostKind(p.Kind),
		Status: gen.PostStatus(p.Status),
		Consent: gen.Consent{
			Confirmed:   p.ConsentConfirmedAt.Valid,
			Note:        optionalString(p.ConsentNote),
			ConfirmedAt: optionalTime(p.ConsentConfirmedAt.Time, p.ConsentConfirmedAt.Valid),
			ConfirmedBy: uuidView(p.ConsentConfirmedBy),
		},
		ScheduledAt:  optionalTime(p.ScheduledAt.Time, p.ScheduledAt.Valid),
		ReviewNote:   optionalString(p.ReviewNote),
		AuthorId:     uuidView(p.AuthorID),
		ApprovedAt:   optionalTime(p.ApprovedAt.Time, p.ApprovedAt.Valid),
		ApprovedBy:   uuidView(p.ApprovedBy),
		PublishedAt:  optionalTime(p.PublishedAt.Time, p.PublishedAt.Valid),
		Publications: make([]gen.Publication, len(p.Publications)),
		Version:      int(p.Version),
		CreatedAt:    p.CreatedAt.UTC(),
		UpdatedAt:    p.UpdatedAt.UTC(),
	}
	for _, r := range p.Versions {
		switch r.Channel {
		case "website":
			v.Versions.Website = &gen.WebsiteVersion{Enabled: r.Enabled, Slug: optionalString(r.Slug),
				Title: localizedView(r.Title), Excerpt: localizedView(r.Excerpt),
				Body:         (*gen.LocalizedMarkdown)(localizedView(r.Body)),
				CoverImageId: uuidView(r.CoverImageID),
				SeoTitle:     localizedView(r.SeoTitle), SeoDescription: localizedView(r.SeoDescription)}
		case "facebook":
			v.Versions.Facebook = &gen.FacebookVersion{Enabled: r.Enabled, Text: optionalString(r.Text),
				Link: optionalString(r.LinkUrl), ImageIds: uuidsView(r.ImageIds)}
		case "instagram":
			v.Versions.Instagram = &gen.InstagramVersion{Enabled: r.Enabled, Caption: optionalString(r.Text),
				ImageIds: uuidsView(r.ImageIds)}
		case "linkedin":
			v.Versions.Linkedin = &gen.LinkedInVersion{Enabled: r.Enabled, Text: optionalString(r.Text),
				Link: optionalString(r.LinkUrl), ImageIds: uuidsView(r.ImageIds)}
		}
	}
	for i, r := range p.Publications {
		v.Publications[i] = gen.Publication{
			Channel:     gen.Channel(r.Channel),
			Status:      gen.PublicationStatus(r.Status),
			ExternalId:  optionalString(r.ExternalID),
			Permalink:   optionalString(r.Permalink),
			Error:       optionalString(r.Error),
			Attempts:    int(r.Attempts),
			PublishedAt: optionalTime(r.PublishedAt.Time, r.PublishedAt.Valid),
			UpdatedAt:   r.UpdatedAt.UTC(),
		}
	}
	return v
}

func postSummaryView(r db.ListPostsRow) gen.PostSummary {
	v := gen.PostSummary{
		Id:          openapi_types.UUID(r.ID.Bytes),
		Title:       r.Title,
		Kind:        gen.PostKind(r.Kind),
		Status:      gen.PostStatus(r.Status),
		ScheduledAt: optionalTime(r.ScheduledAt.Time, r.ScheduledAt.Valid),
		Channels:    make([]gen.Channel, len(r.Channels)),
		PublishedAt: optionalTime(r.PublishedAt.Time, r.PublishedAt.Valid),
		Version:     int(r.Version),
		CreatedAt:   r.CreatedAt.UTC(),
		UpdatedAt:   r.UpdatedAt.UTC(),
	}
	for i, c := range r.Channels {
		v.Channels[i] = gen.Channel(c)
	}
	v.Publications = make([]gen.PublicationSummary, len(r.PublicationChannels))
	for i, c := range r.PublicationChannels {
		v.Publications[i] = gen.PublicationSummary{Channel: gen.Channel(c),
			Status: gen.PublicationStatus(r.PublicationStatuses[i])}
	}
	return v
}
