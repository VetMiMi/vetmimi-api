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

type Email struct {
	Subject string
	Text    string
	HTML    string
	ReplyTo string
}

type parsed struct {
	text *texttemplate.Template
	html *htmltemplate.Template
}

//go:embed templates/*.tmpl
var templateFiles embed.FS

// Parsed at start-up, so a broken template stops the process instead of a send.
var templates = mustParse(templateFiles)

func Render(kind Kind, locale string, data RenderData) (Email, error) {
	template, ok := templates[string(kind)+"."+locale]
	if !ok {
		return Email{}, fmt.Errorf("comms: no template %s.%s", kind, locale)
	}
	var subject bytes.Buffer
	var text bytes.Buffer
	var html bytes.Buffer
	err := template.text.ExecuteTemplate(&subject, "subject", data)
	if err != nil {
		return Email{}, err
	}
	err = template.text.ExecuteTemplate(&text, "text", data)
	if err != nil {
		return Email{}, err
	}
	err = template.html.ExecuteTemplate(&html, "html", data)
	if err != nil {
		return Email{}, err
	}
	email := Email{
		Subject: strings.Join(strings.Fields(subject.String()), " "),
		Text:    tidy(text.String()),
		HTML:    strings.TrimSpace(html.String()) + "\n",
	}
	return email, nil
}

func mustParse(files fs.FS) map[string]parsed {
	parsedTemplates, err := parseTemplates(files)
	if err != nil {
		panic(err)
	}
	return parsedTemplates
}

func parseTemplates(files fs.FS) (map[string]parsed, error) {
	parsedTemplates := make(map[string]parsed, len(Kinds)*len(Locales))
	for _, locale := range Locales {
		for _, kind := range Kinds {
			template, err := parseTemplate(files, kind, locale)
			if err != nil {
				return nil, err
			}
			parsedTemplates[string(kind)+"."+locale] = template
		}
	}
	return parsedTemplates, nil
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

// tidy trims each line and collapses blank runs, so templates can be indented.
func tidy(text string) string {
	var builder strings.Builder
	blank := true
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" {
			if !blank {
				builder.WriteString("\n")
			}
			blank = true
			continue
		}
		builder.WriteString(line + "\n")
		blank = false
	}
	return strings.TrimRight(builder.String(), "\n") + "\n"
}
