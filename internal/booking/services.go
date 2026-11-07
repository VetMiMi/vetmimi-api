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

// PublicService is all a visitor sees of a service.
type PublicService struct {
	Slug            string   `json:"slug"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	BookingAction   string   `json:"bookingAction"`
	DurationMinutes int      `json:"durationMinutes"`
	Formats         []string `json:"formats"`
	FeeText         string   `json:"feeText,omitempty"`
}

type PublicServices struct {
	BookingEnabled bool            `json:"bookingEnabled"`
	BookingMode    string          `json:"bookingMode"`
	Timezone       string          `json:"timezone"`
	Items          []PublicService `json:"items"`
}

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
	service, err := q.GetService(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errServiceNotFound
	}
	if err != nil {
		return db.Service{}, err
	}
	return service, nil
}

func CreateService(ctx context.Context, q db.Querier, params db.CreateServiceParams) (db.Service, error) {
	if len(params.Formats) == 0 {
		params.Formats = []string{"online"}
	}
	err := checkName(params.Name)
	if err != nil {
		return db.Service{}, err
	}
	service, err := q.CreateService(ctx, params)
	if err != nil {
		return db.Service{}, refusal(err)
	}
	return service, nil
}

// UpdateService leaves booked appointments alone: new durations and buffers apply to future bookings.
func UpdateService(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time,
	change func(*db.UpdateServiceParams)) (db.Service, error) {
	saved, err := GetService(ctx, q, id)
	if err != nil {
		return db.Service{}, err
	}
	if saved.Version != version {
		return db.Service{}, errStaleService
	}
	params := db.UpdateServiceParams{
		ID:                  id,
		Version:             version,
		Now:                 now,
		Slug:                saved.Slug,
		Name:                saved.Name,
		Description:         saved.Description,
		BookingAction:       saved.BookingAction,
		State:               saved.State,
		DurationMinutes:     saved.DurationMinutes,
		BufferBeforeMinutes: saved.BufferBeforeMinutes,
		BufferAfterMinutes:  saved.BufferAfterMinutes,
		Formats:             saved.Formats,
		FeeText:             saved.FeeText,
		PreparationText:     saved.PreparationText,
		SortOrder:           saved.SortOrder,
	}
	change(&params)
	if params.State != saved.State && !slices.Contains(serviceStates[saved.State], params.State) {
		return db.Service{}, invalidServiceState(saved.State, params.State)
	}
	err = checkName(params.Name)
	if err != nil {
		return db.Service{}, err
	}
	service, err := q.UpdateService(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errStaleService
	}
	if err != nil {
		return db.Service{}, refusal(err)
	}
	return service, nil
}

// SetServiceState refuses the current state, since the admin's screen must then be stale.
func SetServiceState(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time,
	state string) (db.Service, error) {
	saved, err := GetService(ctx, q, id)
	if err != nil {
		return db.Service{}, err
	}
	if saved.State == state {
		return db.Service{}, invalidServiceState(saved.State, state)
	}
	return UpdateService(ctx, q, id, version, now, func(params *db.UpdateServiceParams) { params.State = state })
}

// DeleteService relies on the foreign key, so a booking racing the delete cannot be orphaned.
func DeleteService(ctx context.Context, q db.Querier, id pgtype.UUID) error {
	deleted, err := q.DeleteService(ctx, id)
	if err != nil {
		return refusal(err)
	}
	if deleted == 0 {
		return errServiceNotFound
	}
	return nil
}

// ListPublicServices is empty while public booking is paused.
func ListPublicServices(ctx context.Context, q db.Querier, locale string) (PublicServices, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return PublicServices{}, err
	}
	services := PublicServices{
		BookingEnabled: current.PublicBookingEnabled,
		BookingMode:    current.BookingMode,
		Timezone:       current.Timezone,
		Items:          []PublicService{},
	}
	if !current.PublicBookingEnabled {
		return services, nil
	}
	rows, err := q.ListPublicBookableServices(ctx)
	if err != nil {
		return PublicServices{}, err
	}
	for _, row := range rows {
		service := PublicService{
			Slug:            row.Slug,
			Name:            inLocale(row.Name, locale),
			Description:     inLocale(row.Description, locale),
			BookingAction:   row.BookingAction,
			DurationMinutes: int(row.DurationMinutes.Int32),
			Formats:         row.Formats,
			FeeText:         inLocale(row.FeeText, locale),
		}
		services.Items = append(services.Items, service)
	}
	return services, nil
}

func invalidServiceState(from, to string) error {
	return apperr.New(apperr.InvalidTransition, "A "+from+" service cannot become "+to+".")
}

// checkName requires English, which every email and the admin fall back to.
func checkName(name json.RawMessage) error {
	var text map[string]string
	if json.Unmarshal(name, &text) != nil || strings.TrimSpace(text["en"]) == "" {
		refused := unprocessable("/name/en", "is required")
		return &refused
	}
	return nil
}

func bookable(service db.Service) bool {
	return service.State == "active" && (service.BookingAction == "book" || service.BookingAction == "request") &&
		service.DurationMinutes.Valid
}

// inLocale falls back to English.
func inLocale(raw json.RawMessage, locale string) string {
	var text map[string]string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	translated := text[locale]
	if strings.TrimSpace(translated) != "" {
		return translated
	}
	return text["en"]
}
