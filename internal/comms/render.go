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

//go:embed templates/*.tmpl
var templateFiles embed.FS

type Email struct {
	Subject, Text, HTML string
	ReplyTo             string
}

type parsed struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

// Parsed at start-up, so a broken template stops the process instead of a send.
var templates = mustParse(templateFiles)

func mustParse(files fs.FS) map[string]parsed {
	t, err := parseTemplates(files)
	if err != nil {
		panic(err)
	}
	return t
}

func parseTemplates(files fs.FS) (map[string]parsed, error) {
	out := make(map[string]parsed, len(Kinds)*len(Locales))
	for _, locale := range Locales {
		for _, kind := range Kinds {
			p, err := parseTemplate(files, kind, locale)
			if err != nil {
				return nil, err
			}
			out[string(kind)+"."+locale] = p
		}
	}
	return out, nil
}

func parseTemplate(files fs.FS, kind Kind, locale string) (parsed, error) {
	shared := "templates/_shared." + locale + ".tmpl"
	name := "templates/" + string(kind) + "." + locale + ".tmpl"
	text, err := texttemplate.ParseFS(files, shared, name)
	if err != nil {
		return parsed{}, fmt.Errorf("comms: template %s.%s: %w", kind, locale, err)
	}
	html, err := htmltemplate.ParseFS(files, shared, name)
	if err != nil {
		return parsed{}, fmt.Errorf("comms: template %s.%s: %w", kind, locale, err)
	}
	for _, block := range []string{"subject", "text"} {
		if text.Lookup(block) == nil {
			return parsed{}, fmt.Errorf("comms: template %s.%s defines no %s", kind, locale, block)
		}
	}
	if html.Lookup("html") == nil {
		return parsed{}, fmt.Errorf("comms: template %s.%s defines no html", kind, locale)
	}
	return parsed{text, html}, nil
}

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

// tidy trims each line and collapses blank runs, so templates can be indented.
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
