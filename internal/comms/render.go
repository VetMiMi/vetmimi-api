package comms

import (
	"bytes"
	"embed"
	"fmt"
	htmltemplate "html/template"
	"io/fs"
	"strings"
	texttemplate "text/template"
)

// Each templates/<kind>.<locale>.tmpl defines subject, text and html, and
// may use the blocks templates/_shared.<locale>.tmpl defines.
//
//go:embed templates/*.tmpl
var templateFiles embed.FS

// Email is a rendered message.
type Email struct {
	Subject, Text, HTML string
	// ReplyTo is settings.contact_email, Daw Mi's approved address.
	ReplyTo string
}

type parsed struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

// templates are parsed once, when the process starts, so a missing or broken
// template stops start-up instead of failing a send.
var templates = mustParse(templateFiles)

func mustParse(files fs.FS) map[string]parsed {
	t, err := parseTemplates(files)
	if err != nil {
		panic(err)
	}
	return t
}

// parseTemplates parses every kind in every locale and fails on the first
// one missing.
func parseTemplates(files fs.FS) (map[string]parsed, error) {
	out := make(map[string]parsed, len(Kinds)*len(Locales))
	for _, locale := range Locales {
		shared := "templates/_shared." + locale + ".tmpl"
		for _, kind := range Kinds {
			name := "templates/" + string(kind) + "." + locale + ".tmpl"
			text, err := texttemplate.ParseFS(files, shared, name)
			if err != nil {
				return nil, fmt.Errorf("comms: template %s.%s: %w", kind, locale, err)
			}
			html, err := htmltemplate.ParseFS(files, shared, name)
			if err != nil {
				return nil, fmt.Errorf("comms: template %s.%s: %w", kind, locale, err)
			}
			for _, block := range []string{"subject", "text"} {
				if text.Lookup(block) == nil {
					return nil, fmt.Errorf("comms: template %s.%s defines no %s", kind, locale, block)
				}
			}
			if html.Lookup("html") == nil {
				return nil, fmt.Errorf("comms: template %s.%s defines no html", kind, locale)
			}
			out[string(kind)+"."+locale] = parsed{text, html}
		}
	}
	return out, nil
}

// Render renders kind in locale with data. The subject and plain text come
// from text/template, the HTML from html/template, which escapes every value.
func Render(kind Kind, locale string, data RenderData) (Email, error) {
	t, ok := templates[string(kind)+"."+locale]
	if !ok {
		return Email{}, fmt.Errorf("comms: no template %s.%s", kind, locale)
	}
	var subject, text, html bytes.Buffer
	if err := t.text.ExecuteTemplate(&subject, "subject", data); err != nil {
		return Email{}, err
	}
	if err := t.text.ExecuteTemplate(&text, "text", data); err != nil {
		return Email{}, err
	}
	if err := t.html.ExecuteTemplate(&html, "html", data); err != nil {
		return Email{}, err
	}
	return Email{
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		Text:    tidy(text.String()),
		HTML:    strings.TrimSpace(html.String()) + "\n",
	}, nil
}

// tidy trims each line and keeps at most one blank line in a row, so
// templates can be indented and use {{if}} freely.
func tidy(s string) string {
	var b strings.Builder
	blank := true
	for line := range strings.Lines(s) {
		line = strings.TrimSpace(line)
		if line == "" {
			if !blank {
				b.WriteString("\n")
			}
			blank = true
			continue
		}
		b.WriteString(line + "\n")
		blank = false
	}
	return strings.TrimRight(b.String(), "\n") + "\n"
}
