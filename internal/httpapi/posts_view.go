package httpapi

import (
	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/content"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func consentInput(c *gen.ConsentInput) *content.Consent {
	if c == nil {
		return nil
	}
	return &content.Consent{Confirmed: c.Confirmed, Note: deref(c.Note)}
}

// versionsInput is one row per channel the request names; the caption of an
// Instagram version is stored as its text.
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

func optionalUUID(id *openapi_types.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return uuid(*id)
}

func uuidView(id pgtype.UUID) *openapi_types.UUID {
	if !id.Valid {
		return nil
	}
	return new(openapi_types.UUID(id.Bytes))
}

func uuids(ids *[]openapi_types.UUID) []pgtype.UUID {
	out := []pgtype.UUID{}
	for _, id := range deref(ids) {
		out = append(out, uuid(id))
	}
	return out
}

func uuidsView(ids []pgtype.UUID) *[]openapi_types.UUID {
	out := make([]openapi_types.UUID, len(ids))
	for i, id := range ids {
		out[i] = openapi_types.UUID(id.Bytes)
	}
	return &out
}
