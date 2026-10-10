package httpapi

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/assistant"
	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// GetAIStatus lets the portal hide the assistant while it is off.
func (s *server) GetAIStatus(_ context.Context, _ gen.GetAIStatusRequestObject) (gen.GetAIStatusResponseObject, error) {
	return gen.GetAIStatus200JSONResponse{Enabled: s.Assistant.Enabled()}, nil
}

// SuggestPostVersions saves nothing.
func (s *server) SuggestPostVersions(ctx context.Context, req gen.SuggestPostVersionsRequestObject) (gen.SuggestPostVersionsResponseObject, error) {
	p, err := content.GetPost(ctx, db.New(s.Pool), uuid(req.PostId))
	if err != nil {
		return nil, err
	}
	r := assistant.Request{Language: "en"}
	if req.Body.Language != nil {
		r.Language = string(*req.Body.Language)
	}
	for _, ch := range req.Body.Channels {
		r.Channels = append(r.Channels, string(ch))
	}
	got, err := s.Assistant.Suggest(ctx, p, r)
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "post_suggested", "request_id", RequestID(ctx), "post_id", p.ID.String(),
		"channels", r.Channels)
	out := gen.SuggestPostVersions200JSONResponse{}
	if got.Facebook != nil {
		out.Facebook = &gen.SuggestedText{Text: got.Facebook.Text}
	}
	if got.Instagram != nil {
		out.Instagram = &gen.SuggestedInstagram{Caption: got.Instagram.Caption, Hashtags: got.Instagram.Hashtags}
	}
	if got.LinkedIn != nil {
		out.Linkedin = &gen.SuggestedText{Text: got.LinkedIn.Text}
	}
	if w := got.Website; w != nil {
		out.Website = &gen.SuggestedWebsite{Language: gen.SuggestedWebsiteLanguage(r.Language), Title: w.Title,
			Excerpt: w.Excerpt, Body: w.Body}
	}
	return out, nil
}
