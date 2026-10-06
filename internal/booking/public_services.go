package booking

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// PublicService is what a visitor sees of a bookable service, its text in
// one locale. Nothing else about the service leaves the API.
type PublicService struct {
	Slug            string
	Name            string
	Description     string
	BookingAction   string
	DurationMinutes int
	Formats         []string
	FeeText         string
}

// PublicServices is the site's first booking step: whether booking is open,
// in which mode and timezone, and what can be booked.
type PublicServices struct {
	BookingEnabled bool
	BookingMode    string
	Timezone       string
	Items          []PublicService
}

// ListPublicServices lists the active services a visitor can book or
// request, in the admin's order, with text in locale or else English. While
// public booking is paused the list is empty; nothing is changed by pausing
// (Booking & Admin UX, section 19).
func ListPublicServices(ctx context.Context, q db.Querier, locale string) (PublicServices, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return PublicServices{}, err
	}
	out := PublicServices{BookingEnabled: cur.PublicBookingEnabled, BookingMode: cur.BookingMode,
		Timezone: cur.Timezone, Items: []PublicService{}}
	if !cur.PublicBookingEnabled {
		return out, nil
	}
	rows, err := q.ListPublicBookableServices(ctx)
	if err != nil {
		return PublicServices{}, err
	}
	for _, r := range rows {
		out.Items = append(out.Items, PublicService{
			Slug:            r.Slug,
			Name:            inLocale(r.Name, locale),
			Description:     inLocale(r.Description, locale),
			BookingAction:   r.BookingAction,
			DurationMinutes: int(r.DurationMinutes.Int32),
			Formats:         r.Formats,
			FeeText:         inLocale(r.FeeText, locale),
		})
	}
	return out, nil
}

// inLocale is localized text in locale, or in English where that is blank.
func inLocale(raw json.RawMessage, locale string) string {
	var text map[string]string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	if t := text[locale]; strings.TrimSpace(t) != "" {
		return t
	}
	return text["en"]
}
