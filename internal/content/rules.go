package content

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
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
// approved, as JSON pointers into the post.
func checkApproval(p db.Post, versions []db.PostVersion) []apperr.FieldError {
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
