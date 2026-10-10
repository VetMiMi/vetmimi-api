package content

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// The platform limits a channel version must meet before approval. Drafts
// may run over them, since an AI-suggested version is often edited down.
const (
	instagramCaptionMax  = 2200
	instagramHashtagsMax = 30
	linkedInTextMax      = 3000
)

var hashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

// checkApproval lists everything that keeps p, with versions, from being
// approved, as JSON pointers into the post. inLibrary holds the media ids
// that exist.
func checkApproval(p db.Post, versions []db.PostVersion, inLibrary map[pgtype.UUID]bool) []apperr.FieldError {
	var problems []apperr.FieldError
	add := func(field, message string) {
		problems = append(problems, apperr.FieldError{Field: field, Message: message})
	}
	if p.Kind == "true_story" && !p.ConsentConfirmedAt.Valid {
		add("/consent/confirmed", "must be confirmed for a True Story")
	}
	enabled := 0
	for _, v := range versions {
		if !v.Enabled {
			continue
		}
		enabled++
		at := "/versions/" + v.Channel
		if v.CoverImageID.Valid && !inLibrary[v.CoverImageID] {
			add(at+"/coverImageId", "is not in the media library")
		}
		for i, id := range v.ImageIds {
			if !inLibrary[id] {
				add(at+"/imageIds/"+strconv.Itoa(i), "is not in the media library")
			}
		}
		switch v.Channel {
		case "website":
			if !v.Slug.Valid {
				add(at+"/slug", "is required")
			}
			for _, f := range []struct {
				name string
				text json.RawMessage
			}{{"title", v.Title}, {"excerpt", v.Excerpt}, {"body", v.Body}} {
				if blank(english(f.text)) {
					add(at+"/"+f.name+"/en", "is required")
				}
			}
		case "facebook":
			if blank(v.Text.String) {
				add(at+"/text", "is required")
			}
		case "instagram":
			if len(v.ImageIds) == 0 {
				add(at+"/imageIds", "needs at least one image")
			}
			if utf8.RuneCountInString(v.Text.String) > instagramCaptionMax {
				add(at+"/caption", "must be at most 2,200 characters")
			}
			if len(hashtag.FindAllString(v.Text.String, -1)) > instagramHashtagsMax {
				add(at+"/caption", "must have at most 30 hashtags")
			}
		case "linkedin":
			if blank(v.Text.String) {
				add(at+"/text", "is required")
			}
			if utf8.RuneCountInString(v.Text.String) > linkedInTextMax {
				add(at+"/text", "must be at most 3,000 characters")
			}
		}
	}
	if enabled == 0 {
		add("/versions", "needs at least one enabled channel")
	}
	return problems
}

// mediaInLibrary is the set of media ids the enabled versions use that
// exist in the library.
func mediaInLibrary(ctx context.Context, q db.Querier, versions []db.PostVersion) (map[pgtype.UUID]bool, error) {
	var ids []pgtype.UUID
	for _, v := range versions {
		if !v.Enabled {
			continue
		}
		if v.CoverImageID.Valid {
			ids = append(ids, v.CoverImageID)
		}
		ids = append(ids, v.ImageIds...)
	}
	found, err := q.ExistingMedia(ctx, ids)
	if err != nil {
		return nil, err
	}
	set := make(map[pgtype.UUID]bool, len(found))
	for _, id := range found {
		set[id] = true
	}
	return set, nil
}

func english(raw json.RawMessage) string {
	return inLocale(raw, "en")
}

// inLocale is the text raw holds for locale, or its English when it has
// none for locale.
func inLocale(raw json.RawMessage, locale string) string {
	var text map[string]string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	if s := text[locale]; !blank(s) {
		return s
	}
	return text["en"]
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }
