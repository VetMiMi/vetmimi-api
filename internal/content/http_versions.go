package content

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// versionsInput is one version per channel; a channel left out keeps its version.
type versionsInput struct {
	Website   *websiteInput   `json:"website"`
	Facebook  *facebookInput  `json:"facebook"`
	Instagram *instagramInput `json:"instagram"`
	LinkedIn  *linkedInInput  `json:"linkedin"`
}

type localizedInput struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=20000"`
	My *string `json:"my,omitempty" binding:"omitnil,max=20000"`
}

type markdownInput struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=100000"`
	My *string `json:"my,omitempty" binding:"omitnil,max=100000"`
}

type websiteInput struct {
	Enabled        *bool           `json:"enabled" binding:"required"`
	Slug           *string         `json:"slug" binding:"omitnil,slug,max=120"`
	Title          *localizedInput `json:"title"`
	Excerpt        *localizedInput `json:"excerpt"`
	Body           *markdownInput  `json:"body"`
	CoverImageID   *string         `json:"coverImageId" binding:"omitnil,uuid"`
	SeoTitle       *localizedInput `json:"seoTitle"`
	SeoDescription *localizedInput `json:"seoDescription"`
}

type facebookInput struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Text     *string  `json:"text" binding:"omitnil,max=10000"`
	Link     *string  `json:"link" binding:"omitnil,max=2000"`
	ImageIDs []string `json:"imageIds" binding:"max=10,dive,uuid"`
}

type instagramInput struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Caption  *string  `json:"caption" binding:"omitnil,max=10000"`
	ImageIDs []string `json:"imageIds" binding:"max=10,dive,uuid"`
}

type linkedInInput struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Text     *string  `json:"text" binding:"omitnil,max=10000"`
	Link     *string  `json:"link" binding:"omitnil,max=2000"`
	ImageIDs []string `json:"imageIds" binding:"max=1,dive,uuid"`
}

// versionParams is one row per channel the request names; an Instagram caption is stored as its text.
func versionParams(v *versionsInput) []db.SavePostVersionParams {
	if v == nil {
		return nil
	}
	var out []db.SavePostVersionParams
	if w := v.Website; w != nil {
		out = append(out, db.SavePostVersionParams{Channel: "website", Enabled: *w.Enabled,
			Slug: textOf(w.Slug), Title: w.Title.raw(), Excerpt: w.Excerpt.raw(), Body: w.Body.raw(),
			CoverImageID: optionalUUID(w.CoverImageID), SeoTitle: w.SeoTitle.raw(), SeoDescription: w.SeoDescription.raw()})
	}
	if f := v.Facebook; f != nil {
		out = append(out, db.SavePostVersionParams{Channel: "facebook", Enabled: *f.Enabled,
			Text: textOf(f.Text), LinkUrl: textOf(f.Link), ImageIds: uuids(f.ImageIDs)})
	}
	if i := v.Instagram; i != nil {
		out = append(out, db.SavePostVersionParams{Channel: "instagram", Enabled: *i.Enabled,
			Text: textOf(i.Caption), ImageIds: uuids(i.ImageIDs)})
	}
	if l := v.LinkedIn; l != nil {
		out = append(out, db.SavePostVersionParams{Channel: "linkedin", Enabled: *l.Enabled,
			Text: textOf(l.Text), LinkUrl: textOf(l.Link), ImageIds: uuids(l.ImageIDs)})
	}
	return out
}

// raw stores text per locale as written; a missing one stays NULL.
func (t *localizedInput) raw() json.RawMessage {
	if t == nil {
		return nil
	}
	b, _ := json.Marshal(t) // two optional strings always marshal
	return b
}

func (t *markdownInput) raw() json.RawMessage {
	if t == nil {
		return nil
	}
	b, _ := json.Marshal(t)
	return b
}

// textOf keeps a sent empty string apart from a missing one.
func textOf(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// optionalUUID and uuids parse ids the binding has already checked.
func optionalUUID(s *string) pgtype.UUID {
	if s == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: uuid.MustParse(*s), Valid: true}
}

func uuids(ids []string) []pgtype.UUID {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		out[i] = pgtype.UUID{Bytes: uuid.MustParse(id), Valid: true}
	}
	return out
}

type versionsResponse struct {
	Website   *websiteResponse   `json:"website,omitempty"`
	Facebook  *linkPostResponse  `json:"facebook,omitempty"`
	Instagram *instagramResponse `json:"instagram,omitempty"`
	LinkedIn  *linkPostResponse  `json:"linkedin,omitempty"`
}

// websiteResponse answers the localized text as stored: the shape it was saved in.
type websiteResponse struct {
	Enabled        bool            `json:"enabled"`
	Slug           pgtype.Text     `json:"slug,omitzero"`
	Title          json.RawMessage `json:"title,omitempty"`
	Excerpt        json.RawMessage `json:"excerpt,omitempty"`
	Body           json.RawMessage `json:"body,omitempty"`
	CoverImageID   pgtype.UUID     `json:"coverImageId,omitzero"`
	SeoTitle       json.RawMessage `json:"seoTitle,omitempty"`
	SeoDescription json.RawMessage `json:"seoDescription,omitempty"`
}

// linkPostResponse is a Facebook or LinkedIn version.
type linkPostResponse struct {
	Enabled  bool          `json:"enabled"`
	Text     pgtype.Text   `json:"text,omitzero"`
	Link     pgtype.Text   `json:"link,omitzero"`
	ImageIDs []pgtype.UUID `json:"imageIds"`
}

type instagramResponse struct {
	Enabled  bool          `json:"enabled"`
	Caption  pgtype.Text   `json:"caption,omitzero"`
	ImageIDs []pgtype.UUID `json:"imageIds"`
}

func versionsView(rows []db.PostVersion) versionsResponse {
	var v versionsResponse
	for _, r := range rows {
		images := append([]pgtype.UUID{}, r.ImageIds...)
		switch r.Channel {
		case "website":
			v.Website = &websiteResponse{Enabled: r.Enabled, Slug: r.Slug, Title: r.Title, Excerpt: r.Excerpt,
				Body: r.Body, CoverImageID: r.CoverImageID, SeoTitle: r.SeoTitle, SeoDescription: r.SeoDescription}
		case "facebook":
			v.Facebook = &linkPostResponse{Enabled: r.Enabled, Text: r.Text, Link: r.LinkUrl, ImageIDs: images}
		case "instagram":
			v.Instagram = &instagramResponse{Enabled: r.Enabled, Caption: r.Text, ImageIDs: images}
		case "linkedin":
			v.LinkedIn = &linkPostResponse{Enabled: r.Enabled, Text: r.Text, Link: r.LinkUrl, ImageIDs: images}
		}
	}
	return v
}
