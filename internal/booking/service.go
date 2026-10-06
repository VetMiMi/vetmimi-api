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

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// serviceStates is every service state change allowed (docs/data-model.md,
// "services"). Archiving hides a service without deleting its history;
// bringing it back lands on paused, so it is never bookable by accident.
var serviceStates = map[string][]string{
	"active":   {"paused", "archived"},
	"paused":   {"active", "archived"},
	"archived": {"paused"},
}

var errServiceNotFound = apperr.New(apperr.NotFound, "No service has this id.")

// ListServices lists every service, paused and archived included, in the
// order the admin arranged them.
func ListServices(ctx context.Context, q db.Querier) ([]db.Service, error) {
	return q.ListServices(ctx)
}

// GetService reads one service.
func GetService(ctx context.Context, q db.Querier, id pgtype.UUID) (db.Service, error) {
	s, err := q.GetService(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errServiceNotFound
	}
	return s, err
}

// CreateService saves a new service; it starts active, offered online unless
// p says otherwise.
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

// UpdateService applies change to the service as of version and saves it.
// New durations and buffers apply to future bookings only: no appointment is
// touched. A state change must be in serviceStates.
func UpdateService(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time,
	change func(*db.UpdateServiceParams)) (db.Service, error) {
	cur, err := GetService(ctx, q, id)
	if err != nil {
		return db.Service{}, err
	}
	if cur.Version != version {
		return db.Service{}, staleService
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
		return db.Service{}, staleService
	}
	return s, refusal(err)
}

var staleService = apperr.New(apperr.StaleVersion, "The service changed since it was read; reload it.")

// SetServiceState moves the service as of version to state, as pause and
// resume do. Appointments and availability rows are untouched. Asking for
// the state it is already in is refused, since the admin's screen is stale.
func SetServiceState(ctx context.Context, q db.Querier, id pgtype.UUID, version int32, now time.Time, state string) (db.Service, error) {
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

// DeleteService deletes a service nothing refers to. One with appointments
// is refused as in_use by the appointments foreign key, so a booking racing
// the delete cannot be orphaned.
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

// checkName requires English text in name, which every email and the admin
// fall back to.
func checkName(name json.RawMessage) error {
	var text map[string]string
	if json.Unmarshal(name, &text) != nil || strings.TrimSpace(text["en"]) == "" {
		e := unprocessable("/name/en", "is required")
		return &e
	}
	return nil
}
