package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Override adds hours to one local date ("open") or replaces its weekly rules ("replace").
type Override struct {
	OnDate time.Time
	Kind   string
	Period Period
	Note   pgtype.Text
}

var errOverrideNotFound = apperr.New(apperr.NotFound, "No override has this id.")

func ListOverrides(ctx context.Context, q db.Querier, first, last time.Time) ([]db.AvailabilityOverride, error) {
	params := db.ListAvailabilityOverridesParams{
		FromDate: pgtype.Date{Time: first, Valid: true},
		ToDate:   pgtype.Date{Time: last, Valid: true},
	}
	return q.ListAvailabilityOverrides(ctx, params)
}

func CreateOverride(ctx context.Context, pool *pgxpool.Pool, override Override,
	by pgtype.UUID) (db.AvailabilityOverride, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	err = checkOverride(ctx, q, override)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	params := db.CreateAvailabilityOverrideParams{
		OnDate:    pgtype.Date{Time: override.OnDate, Valid: true},
		Kind:      override.Kind,
		Period:    override.Period.tstzrange(),
		Note:      override.Note,
		CreatedBy: by,
	}
	row, err := q.CreateAvailabilityOverride(ctx, params)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	return row, nil
}

func UpdateOverride(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, override Override,
	now time.Time) (db.AvailabilityOverride, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	err = checkOverride(ctx, q, override)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	params := db.UpdateAvailabilityOverrideParams{
		ID:     id,
		OnDate: pgtype.Date{Time: override.OnDate, Valid: true},
		Kind:   override.Kind,
		Period: override.Period.tstzrange(),
		Note:   override.Note,
		Now:    now,
	}
	row, err := q.UpdateAvailabilityOverride(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AvailabilityOverride{}, errOverrideNotFound
	}
	if err != nil {
		return db.AvailabilityOverride{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return db.AvailabilityOverride{}, err
	}
	return row, nil
}

func DeleteOverride(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return err
	}
	deleted, err := q.DeleteAvailabilityOverride(ctx, id)
	if err != nil {
		return err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return errOverrideNotFound
	}
	return nil
}

// checkOverride requires a non-empty period inside its local date, ending at the next midnight at the latest.
func checkOverride(ctx context.Context, q db.Querier, override Override) error {
	_, loc, err := PracticeZone(ctx, q)
	if err != nil {
		return err
	}
	day := LocalDays(override.OnDate, override.OnDate, loc)
	period := override.Period
	var fields []apperr.FieldError
	if period.Start.Before(day.Start) || !period.Start.Before(day.End) {
		fields = append(fields, apperr.FieldError{Field: "/startsAt", Message: "must fall on onDate in the practice timezone"})
	}
	if !period.End.After(period.Start) {
		fields = append(fields, apperr.FieldError{Field: "/endsAt", Message: "must be later than startsAt"})
	} else if period.End.After(day.End) {
		fields = append(fields, apperr.FieldError{Field: "/endsAt", Message: "must fall on onDate in the practice timezone"})
	}
	if len(fields) > 0 {
		return &apperr.Error{
			Code:   apperr.ActionNotAllowed,
			Detail: "The override is outside its date.",
			Fields: fields,
		}
	}
	return nil
}
