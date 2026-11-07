package content

import (
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// versionsBody is one version per channel; a channel left out keeps its version.
type versionsBody struct {
	Website   *websiteBody   `json:"website"`
	Facebook  *facebookBody  `json:"facebook"`
	Instagram *instagramBody `json:"instagram"`
	LinkedIn  *linkedInBody  `json:"linkedin"`
}

type localizedBody struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=20000"`
	My *string `json:"my,omitempty" binding:"omitnil,max=20000"`
}

type markdownBody struct {
	En *string `json:"en,omitempty" binding:"omitnil,max=100000"`
	My *string `json:"my,omitempty" binding:"omitnil,max=100000"`
}

type websiteBody struct {
	Enabled        *bool          `json:"enabled" binding:"required"`
	Slug           *string        `json:"slug" binding:"omitnil,slug,max=120"`
	Title          *localizedBody `json:"title"`
	Excerpt        *localizedBody `json:"excerpt"`
	Body           *markdownBody  `json:"body"`
	CoverImageID   *string        `json:"coverImageId" binding:"omitnil,uuid"`
	SeoTitle       *localizedBody `json:"seoTitle"`
	SeoDescription *localizedBody `json:"seoDescription"`
}

type facebookBody struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Text     *string  `json:"text" binding:"omitnil,max=10000"`
	Link     *string  `json:"link" binding:"omitnil,max=2000"`
	ImageIDs []string `json:"imageIds" binding:"max=10,dive,uuid"`
}

type instagramBody struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Caption  *string  `json:"caption" binding:"omitnil,max=10000"`
	ImageIDs []string `json:"imageIds" binding:"max=10,dive,uuid"`
}

type linkedInBody struct {
	Enabled  *bool    `json:"enabled" binding:"required"`
	Text     *string  `json:"text" binding:"omitnil,max=10000"`
	Link     *string  `json:"link" binding:"omitnil,max=2000"`
	ImageIDs []string `json:"imageIds" binding:"max=1,dive,uuid"`
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

// newVersionParams is one row per channel the request names; an Instagram caption is stored as its text.
func newVersionParams(body *versionsBody) []db.SavePostVersionParams {
	if body == nil {
		return nil
	}
	var params []db.SavePostVersionParams
	if body.Website != nil {
		website := body.Website
		params = append(params, db.SavePostVersionParams{
			Channel:        "website",
			Enabled:        *website.Enabled,
			Slug:           textOrNull(website.Slug),
			Title:          website.Title.json(),
			Excerpt:        website.Excerpt.json(),
			Body:           website.Body.json(),
			CoverImageID:   uuidOrNull(website.CoverImageID),
			SeoTitle:       website.SeoTitle.json(),
			SeoDescription: website.SeoDescription.json(),
		})
	}
	if body.Facebook != nil {
		facebook := body.Facebook
		params = append(params, db.SavePostVersionParams{
			Channel:  "facebook",
			Enabled:  *facebook.Enabled,
			Text:     textOrNull(facebook.Text),
			LinkUrl:  textOrNull(facebook.Link),
			ImageIds: toUUIDs(facebook.ImageIDs),
		})
	}
	if body.Instagram != nil {
		instagram := body.Instagram
		params = append(params, db.SavePostVersionParams{
			Channel:  "instagram",
			Enabled:  *instagram.Enabled,
			Text:     textOrNull(instagram.Caption),
			ImageIds: toUUIDs(instagram.ImageIDs),
		})
	}
	if body.LinkedIn != nil {
		linkedIn := body.LinkedIn
		params = append(params, db.SavePostVersionParams{
			Channel:  "linkedin",
			Enabled:  *linkedIn.Enabled,
			Text:     textOrNull(linkedIn.Text),
			LinkUrl:  textOrNull(linkedIn.Link),
			ImageIds: toUUIDs(linkedIn.ImageIDs),
		})
	}
	return params
}

func newVersionsResponse(versions []db.PostVersion) versionsResponse {
	var response versionsResponse
	for _, version := range versions {
		imageIDs := append([]pgtype.UUID{}, version.ImageIds...)
		switch version.Channel {
		case "website":
			response.Website = &websiteResponse{
				Enabled:        version.Enabled,
				Slug:           version.Slug,
				Title:          version.Title,
				Excerpt:        version.Excerpt,
				Body:           version.Body,
				CoverImageID:   version.CoverImageID,
				SeoTitle:       version.SeoTitle,
				SeoDescription: version.SeoDescription,
			}
		case "facebook":
			response.Facebook = &linkPostResponse{
				Enabled:  version.Enabled,
				Text:     version.Text,
				Link:     version.LinkUrl,
				ImageIDs: imageIDs,
			}
		case "instagram":
			response.Instagram = &instagramResponse{
				Enabled:  version.Enabled,
				Caption:  version.Text,
				ImageIDs: imageIDs,
			}
		case "linkedin":
			response.LinkedIn = &linkPostResponse{
				Enabled:  version.Enabled,
				Text:     version.Text,
				Link:     version.LinkUrl,
				ImageIDs: imageIDs,
			}
		}
	}
	return response
}

// json stores the text per locale as written; a missing body stays NULL.
// Marshalling two optional strings cannot fail, so the error is not checked.
func (text *localizedBody) json() json.RawMessage {
	if text == nil {
		return nil
	}
	raw, _ := json.Marshal(text)
	return raw
}

func (text *markdownBody) json() json.RawMessage {
	if text == nil {
		return nil
	}
	raw, _ := json.Marshal(text)
	return raw
}

// textOrNull keeps a sent empty string apart from a missing one.
func textOrNull(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// uuidOrNull and toUUIDs parse ids the binding has already checked.
func uuidOrNull(s *string) pgtype.UUID {
	if s == nil {
		return pgtype.UUID{}
	}
	return pgtype.UUID{Bytes: uuid.MustParse(*s), Valid: true}
}

func toUUIDs(ids []string) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		result[i] = pgtype.UUID{Bytes: uuid.MustParse(id), Valid: true}
	}
	return result
}
