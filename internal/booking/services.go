package booking

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// An archived service comes back paused, so it is never bookable by accident.
var serviceStates = map[string][]string{
	"active":   {"paused", "archived"},
	"paused":   {"active", "archived"},
	"archived": {"paused"},
}

var (
	errServiceNotFound = apperr.New(apperr.NotFound, "No service has this id.")
	errStaleService    = apperr.New(apperr.StaleVersion, "The service changed since it was read; reload it.")
)

func ListServices(ctx context.Context, q db.Querier) ([]db.Service, error) {
	return q.ListServices(ctx)
}

func GetService(ctx context.Context, q db.Querier, id pgtype.UUID) (db.Service, error) {
	s, err := q.GetService(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errServiceNotFound
	}
	return s, err
}

func CreateService(ctx context.Context, q db.Querier, p db.CreateServiceParams) (db.Service, error) {
	if len(p.Formats) == 0 {
		p.Formats = []string{"online"}
	}
	if err := checkName(p.Name); err != nil {
		return db.Service{}, err
	}
	s, err := q.CreateService(ctx, p)
	return s, refusal(err)
}

// UpdateService leaves booked appointments alone: new durations and buffers apply to future bookings.
func UpdateService(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time,
	change func(*db.UpdateServiceParams)) (db.Service, error) {
	cur, err := GetService(ctx, q, id)
	if err != nil {
		return db.Service{}, err
	}
	if cur.Version != version {
		return db.Service{}, errStaleService
	}
	p := db.UpdateServiceParams{
		ID: id, Version: version, Now: now,
		Slug: cur.Slug, Name: cur.Name, Description: cur.Description, BookingAction: cur.BookingAction,
		State: cur.State, DurationMinutes: cur.DurationMinutes, BufferBeforeMinutes: cur.BufferBeforeMinutes,
		BufferAfterMinutes: cur.BufferAfterMinutes, Formats: cur.Formats, FeeText: cur.FeeText,
		PreparationText: cur.PreparationText, SortOrder: cur.SortOrder,
	}
	change(&p)
	if p.State != cur.State && !slices.Contains(serviceStates[cur.State], p.State) {
		return db.Service{}, invalidServiceState(cur.State, p.State)
	}
	if err := checkName(p.Name); err != nil {
		return db.Service{}, err
	}
	s, err := q.UpdateService(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errStaleService
	}
	return s, refusal(err)
}

// SetServiceState refuses the current state, since the admin's screen must then be stale.
func SetServiceState(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time,
	state string) (db.Service, error) {
	cur, err := GetService(ctx, q, id)
	if err != nil {
		return db.Service{}, err
	}
	if cur.State == state {
		return db.Service{}, invalidServiceState(cur.State, state)
	}
	return UpdateService(ctx, q, id, version, now, func(p *db.UpdateServiceParams) { p.State = state })
}

func invalidServiceState(from, to string) error {
	return apperr.New(apperr.InvalidTransition, "A "+from+" service cannot become "+to+".")
}

// DeleteService relies on the foreign key, so a booking racing the delete cannot be orphaned.
func DeleteService(ctx context.Context, q db.Querier, id pgtype.UUID) error {
	n, err := q.DeleteService(ctx, id)
	if err != nil {
		return refusal(err)
	}
	if n == 0 {
		return errServiceNotFound
	}
	return nil
}

// checkName requires English, which every email and the admin fall back to.
func checkName(name json.RawMessage) error {
	var text map[string]string
	if json.Unmarshal(name, &text) != nil || strings.TrimSpace(text["en"]) == "" {
		e := unprocessable("/name/en", "is required")
		return &e
	}
	return nil
}

func bookable(svc db.Service) bool {
	return svc.State == "active" && (svc.BookingAction == "book" || svc.BookingAction == "request") &&
		svc.DurationMinutes.Valid
}

// PublicService is all a visitor sees of a service.
type PublicService struct {
	Slug            string
	Name            string
	Description     string
	BookingAction   string
	DurationMinutes int
	Formats         []string
	FeeText         string
}

type PublicServices struct {
	BookingEnabled bool
	BookingMode    string
	Timezone       string
	Items          []PublicService
}

// ListPublicServices is empty while public booking is paused.
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

// inLocale falls back to English.
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
