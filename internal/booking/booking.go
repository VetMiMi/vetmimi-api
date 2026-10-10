// Package booking schedules appointments. Admins set services.go and availability.go; slots.go turns them into
// free times; requests.go and manual.go create appointments; status.go, reschedule.go and manage.go change them;
// holds.go, reminders.go and purge.go run from worker.go; admin.go and dashboard.go read them for the admin.
// PostgreSQL constraints guard every scheduling rule.
package booking

import (
	"context"
	"errors"
	"expvar"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Period is a half-open span [Start, End), like every tstzrange in the schema.
type Period struct {
	Start, End time.Time
}

func (p Period) tstzrange() pgtype.Range[pgtype.Timestamptz] {
	return pgtype.Range[pgtype.Timestamptz]{
		Lower:     pgtype.Timestamptz{Time: p.Start, Valid: true},
		Upper:     pgtype.Timestamptz{Time: p.End, Valid: true},
		LowerType: pgtype.Inclusive,
		UpperType: pgtype.Exclusive,
		Valid:     true,
	}
}

func (p Period) overlaps(o Period) bool {
	return p.Start.Before(o.End) && o.Start.Before(p.End)
}

func PeriodOf(r pgtype.Range[pgtype.Timestamptz]) Period {
	return Period{Start: r.Lower.Time, End: r.Upper.Time}
}

// inSchedule holds the schedule lock, so availability writes, bookings and admin actions never interleave.
func inSchedule(ctx context.Context, pool *pgxpool.Pool, fn func(q *db.Queries) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.LockSchedule(ctx); err != nil {
			return err
		}
		return fn(q)
	})
}

var refusals = map[string]apperr.Error{
	"appointments_no_overlap": {Code: apperr.SlotUnavailable,
		Detail: "The time overlaps another pending or confirmed appointment."},
	"appointments_service_id_fkey": {Code: apperr.InUse,
		Detail: "The service has appointments; archive it instead."},
	"services_slug_key": {Code: apperr.SlugTaken, Detail: "Another service uses this slug."},
	"services_duration_required": unprocessable("/durationMinutes",
		"is required when bookingAction is book or request"),
	"availability_rules_no_overlap": {Code: apperr.OverlappingPeriod,
		Detail: "The period overlaps another on the same weekday."},
	"availability_rules_end_after_start": unprocessable("/endTime", "must be later than startTime"),
}

func refusal(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	if e, ok := refusals[pgErr.ConstraintName]; ok {
		return &e
	}
	return err
}

func unprocessable(field, message string) apperr.Error {
	return apperr.Error{Code: apperr.ActionNotAllowed, Detail: "A scheduling rule refused the request.",
		Fields: []apperr.FieldError{{Field: field, Message: message}}}
}

// Counters keyed by website, manual or reschedule.
var (
	appointmentsCreated = expvar.NewMap("appointments_created")
	slotConflicts       = expvar.NewMap("slot_conflicts")
)

func countConflict(key string, err error) error {
	var e *apperr.Error
	if errors.As(err, &e) && e.Code == apperr.SlotUnavailable {
		slotConflicts.Add(key, 1)
	}
	return err
}
