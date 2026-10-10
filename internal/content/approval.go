package content

import (
	"context"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Checked at approval only: an AI-suggested draft often runs over and is edited down.
const (
	instagramCaptionMax  = 2200
	instagramHashtagsMax = 30
	linkedInTextMax      = 3000
)

var hashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

// problems names each field as a JSON pointer into the post.
type problems []apperr.FieldError

func (ps *problems) add(field, message string) {
	*ps = append(*ps, apperr.FieldError{Field: field, Message: message})
}

func checkApproval(p db.Post, versions []db.PostVersion, inLibrary map[pgtype.UUID]bool) []apperr.FieldError {
	var ps problems
	if p.Kind == "true_story" && !p.ConsentConfirmedAt.Valid {
		ps.add("/consent/confirmed", "must be confirmed for a True Story")
	}
	enabled := 0
	for _, v := range versions {
		if !v.Enabled {
			continue
		}
		enabled++
		checkImages(&ps, v, inLibrary)
		checkText(&ps, v)
	}
	if enabled == 0 {
		ps.add("/versions", "needs at least one enabled channel")
	}
	return ps
}

func checkImages(ps *problems, v db.PostVersion, inLibrary map[pgtype.UUID]bool) {
	at := "/versions/" + v.Channel
	if v.CoverImageID.Valid && !inLibrary[v.CoverImageID] {
		ps.add(at+"/coverImageId", "is not in the media library")
	}
	for i, id := range v.ImageIds {
		if !inLibrary[id] {
			ps.add(at+"/imageIds/"+strconv.Itoa(i), "is not in the media library")
		}
	}
}

func checkText(ps *problems, v db.PostVersion) {
	at := "/versions/" + v.Channel
	switch v.Channel {
	case "website":
		if !v.Slug.Valid {
			ps.add(at+"/slug", "is required")
		}
		if blank(inLocale(v.Title, "en")) {
			ps.add(at+"/title/en", "is required")
		}
		if blank(inLocale(v.Excerpt, "en")) {
			ps.add(at+"/excerpt/en", "is required")
		}
		if blank(inLocale(v.Body, "en")) {
			ps.add(at+"/body/en", "is required")
		}
	case "facebook":
		if blank(v.Text.String) {
			ps.add(at+"/text", "is required")
		}
	case "instagram":
		if len(v.ImageIds) == 0 {
			ps.add(at+"/imageIds", "needs at least one image")
		}
		if utf8.RuneCountInString(v.Text.String) > instagramCaptionMax {
			ps.add(at+"/caption", "must be at most 2,200 characters")
		}
		if len(hashtag.FindAllString(v.Text.String, -1)) > instagramHashtagsMax {
			ps.add(at+"/caption", "must have at most 30 hashtags")
		}
	case "linkedin":
		if blank(v.Text.String) {
			ps.add(at+"/text", "is required")
		}
		if utf8.RuneCountInString(v.Text.String) > linkedInTextMax {
			ps.add(at+"/text", "must be at most 3,000 characters")
		}
	}
}

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
