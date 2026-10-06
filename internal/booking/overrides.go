package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// Override is a one-off change to one local date's hours: kind open adds
// Period to the date; the replace rows of a date together stand in for its
// weekly periods. Dates are calendar dates: only the year, month and day of
// OnDate count.
type Override struct {
	OnDate time.Time
	Kind   string
	Period Period
	Note   pgtype.Text
}

// defaultWindowDays is how far ahead the admin lists look when not told.
const defaultWindowDays = 62

var errOverrideNotFound = apperr.New(apperr.NotFound, "No override has this id.")

// DateWindow is the local dates a list covers: from and to as given, or
// today and today + 62 days in the practice timezone.
func DateWindow(now time.Time, loc *time.Location, from, to *time.Time) (time.Time, time.Time, error) {
	local := now.In(loc)
	first := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if from != nil {
		first = *from
	}
	last := first.AddDate(0, 0, defaultWindowDays)
	if to != nil {
		last = *to
	}
	if last.Before(first) {
		return time.Time{}, time.Time{}, apperr.Invalid("to is before from.",
			apperr.FieldError{Field: "to", Message: "must not be before from"})
	}
	return first, last, nil
}

// LocalDays is the instants from the start of local date first to the end of
// local date last. A day is 23, 24 or 25 hours, as daylight saving makes it.
func LocalDays(first, last time.Time, loc *time.Location) Period {
	return Period{
		Start: time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc),
		End:   time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc),
	}
}

// ListOverrides lists the overrides on local dates first to last.
func ListOverrides(ctx context.Context, q db.Querier, first, last time.Time) ([]db.AvailabilityOverride, error) {
	return q.ListAvailabilityOverrides(ctx, db.ListAvailabilityOverridesParams{
		FromDate: pgtype.Date{Time: first, Valid: true}, ToDate: pgtype.Date{Time: last, Valid: true}})
}

// CreateOverride saves o, made by an administrator.
func CreateOverride(ctx context.Context, pool *pgxpool.Pool, o Override, by pgtype.UUID) (db.AvailabilityOverride, error) {
	var row db.AvailabilityOverride
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		if err = checkOverride(ctx, q, o); err != nil {
			return err
		}
		row, err = q.CreateAvailabilityOverride(ctx, db.CreateAvailabilityOverrideParams{
			OnDate: pgtype.Date{Time: o.OnDate, Valid: true}, Kind: o.Kind, Period: o.Period.tstzrange(),
			Note: o.Note, CreatedBy: by})
		return err
	})
	return row, err
}

// UpdateOverride replaces an override with o.
func UpdateOverride(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, o Override, now time.Time) (db.AvailabilityOverride, error) {
	var row db.AvailabilityOverride
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		if err = checkOverride(ctx, q, o); err != nil {
			return err
		}
		row, err = q.UpdateAvailabilityOverride(ctx, db.UpdateAvailabilityOverrideParams{
			ID: id, OnDate: pgtype.Date{Time: o.OnDate, Valid: true}, Kind: o.Kind,
			Period: o.Period.tstzrange(), Note: o.Note, Now: now})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AvailabilityOverride{}, errOverrideNotFound
	}
	return row, err
}

// DeleteOverride deletes an override.
func DeleteOverride(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	var n int64
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		n, err = q.DeleteAvailabilityOverride(ctx, id)
		return err
	})
	if err == nil && n == 0 {
		return errOverrideNotFound
	}
	return err
}

// checkOverride requires o's period to be non-empty and to lie inside its
// local date in the practice timezone; it may end at the next midnight.
func checkOverride(ctx context.Context, q db.Querier, o Override) error {
	_, loc, err := PracticeZone(ctx, q)
	if err != nil {
		return err
	}
	day := LocalDays(o.OnDate, o.OnDate, loc)
	var fields []apperr.FieldError
	if o.Period.Start.Before(day.Start) || !o.Period.Start.Before(day.End) {
		fields = append(fields, apperr.FieldError{Field: "/startsAt", Message: "must fall on onDate in the practice timezone"})
	}
	if !o.Period.End.After(o.Period.Start) {
		fields = append(fields, apperr.FieldError{Field: "/endsAt", Message: "must be later than startsAt"})
	} else if o.Period.End.After(day.End) {
		fields = append(fields, apperr.FieldError{Field: "/endsAt", Message: "must fall on onDate in the practice timezone"})
	}
	if len(fields) > 0 {
		return &apperr.Error{Code: apperr.ActionNotAllowed, Detail: "The override is outside its date.", Fields: fields}
	}
	return nil
}

// PracticeZone is the practice timezone's name and location, from settings.
func PracticeZone(ctx context.Context, q db.Querier) (string, *time.Location, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return "", nil, err
	}
	loc, err := current.Location()
	return current.Timezone, loc, err
}
