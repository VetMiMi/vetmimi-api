package content

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Checked only before publishing: an AI-suggested draft often runs over and is edited down.
const (
	instagramCaptionMax  = 2200
	instagramHashtagsMax = 30
	linkedInTextMax      = 3000
)

var hashtag = regexp.MustCompile(`#[\p{L}\p{N}_]+`)

// problems names each field as a JSON pointer into the post.
type problems []apperr.FieldError

// checkReady checks every enabled channel against its platform's rules before the
// post is scheduled or published, and records who cleared it.
func checkReady(ctx context.Context, q *db.Queries, post *db.Post, by pgtype.UUID, now time.Time) error {
	versions, err := q.ListPostVersions(ctx, post.ID)
	if err != nil {
		return err
	}
	inLibrary, err := mediaInLibrary(ctx, q, versions)
	if err != nil {
		return err
	}

	found := findProblems(*post, versions, inLibrary)
	if len(found) > 0 {
		return &apperr.Error{
			Code:   apperr.PublishRequirementsUnmet,
			Detail: "The post is not ready to publish.",
			Fields: found,
		}
	}

	post.ApprovedAt = sql.NullTime{Time: now, Valid: true}
	post.ApprovedBy = by
	return nil
}

func findProblems(post db.Post, versions []db.PostVersion, inLibrary map[pgtype.UUID]bool) problems {
	var found problems
	if post.Kind == "true_story" && !post.ConsentConfirmedAt.Valid {
		found.add("/consent/confirmed", "must be confirmed for a True Story")
	}
	enabled := 0
	for _, version := range versions {
		if !version.Enabled {
			continue
		}
		enabled++
		found.checkImages(version, inLibrary)
		found.checkText(version)
	}
	if enabled == 0 {
		found.add("/versions", "needs at least one enabled channel")
	}
	return found
}

func (found *problems) add(field, message string) {
	*found = append(*found, apperr.FieldError{Field: field, Message: message})
}

func (found *problems) checkImages(version db.PostVersion, inLibrary map[pgtype.UUID]bool) {
	at := "/versions/" + version.Channel
	if version.CoverImageID.Valid && !inLibrary[version.CoverImageID] {
		found.add(at+"/coverImageId", "is not in the media library")
	}
	for i, id := range version.ImageIds {
		if !inLibrary[id] {
			found.add(at+"/imageIds/"+strconv.Itoa(i), "is not in the media library")
		}
	}
}

func (found *problems) checkText(version db.PostVersion) {
	at := "/versions/" + version.Channel
	text := version.Text.String
	switch version.Channel {
	case "website":
		if !version.Slug.Valid {
			found.add(at+"/slug", "is required")
		}
		if blank(inLocale(version.Title, "en")) {
			found.add(at+"/title/en", "is required")
		}
		if blank(inLocale(version.Excerpt, "en")) {
			found.add(at+"/excerpt/en", "is required")
		}
		if blank(inLocale(version.Body, "en")) {
			found.add(at+"/body/en", "is required")
		}
	case "facebook":
		if blank(text) {
			found.add(at+"/text", "is required")
		}
	case "instagram":
		if len(version.ImageIds) == 0 {
			found.add(at+"/imageIds", "needs at least one image")
		}
		if utf8.RuneCountInString(text) > instagramCaptionMax {
			found.add(at+"/caption", "must be at most 2,200 characters")
		}
		if len(hashtag.FindAllString(text, -1)) > instagramHashtagsMax {
			found.add(at+"/caption", "must have at most 30 hashtags")
		}
	case "linkedin":
		if blank(text) {
			found.add(at+"/text", "is required")
		}
		if utf8.RuneCountInString(text) > linkedInTextMax {
			found.add(at+"/text", "must be at most 3,000 characters")
		}
	}
}

func mediaInLibrary(ctx context.Context, q db.Querier, versions []db.PostVersion) (map[pgtype.UUID]bool, error) {
	var ids []pgtype.UUID
	for _, version := range versions {
		if !version.Enabled {
			continue
		}
		if version.CoverImageID.Valid {
			ids = append(ids, version.CoverImageID)
		}
		ids = append(ids, version.ImageIds...)
	}

	found, err := q.ExistingMedia(ctx, ids)
	if err != nil {
		return nil, err
	}
	inLibrary := make(map[pgtype.UUID]bool, len(found))
	for _, id := range found {
		inLibrary[id] = true
	}
	return inLibrary, nil
}
