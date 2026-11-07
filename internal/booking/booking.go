// Package booking schedules appointments: services, weekly hours, free slots, requests and status changes.
// Availability edits, bookings and admin actions take the schedule lock first, so they never interleave;
// service edits rely on their version check. PostgreSQL constraints still guard every scheduling rule.
package booking

import (
	"errors"
	"expvar"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

// Period is a half-open span [Start, End), like every tstzrange in the schema.
type Period struct {
	Start, End time.Time
}

// Counters keyed by website, manual or reschedule.
var (
	appointmentsCreated = expvar.NewMap("appointments_created")
	slotConflicts       = expvar.NewMap("slot_conflicts")
)

var refusals = map[string]apperr.Error{
	"appointments_no_overlap": {
		Code:   apperr.SlotUnavailable,
		Detail: "The time overlaps another pending or confirmed appointment.",
	},
	"appointments_service_id_fkey": {
		Code:   apperr.InUse,
		Detail: "The service has appointments; archive it instead.",
	},
	"services_slug_key": {
		Code:   apperr.SlugTaken,
		Detail: "Another service uses this slug.",
	},
	"services_duration_required": unprocessable("/durationMinutes", "is required when bookingAction is book or request"),
	"availability_rules_no_overlap": {
		Code:   apperr.OverlappingPeriod,
		Detail: "The period overlaps another on the same weekday.",
	},
	"availability_rules_end_after_start": unprocessable("/endTime", "must be later than startTime"),
}

func PeriodOf(span pgtype.Range[pgtype.Timestamptz]) Period {
	return Period{Start: span.Lower.Time, End: span.Upper.Time}
}

func (period Period) tstzrange() pgtype.Range[pgtype.Timestamptz] {
	return pgtype.Range[pgtype.Timestamptz]{
		Lower:     pgtype.Timestamptz{Time: period.Start, Valid: true},
		Upper:     pgtype.Timestamptz{Time: period.End, Valid: true},
		LowerType: pgtype.Inclusive,
		UpperType: pgtype.Exclusive,
		Valid:     true,
	}
}

func (period Period) overlaps(other Period) bool {
	return period.Start.Before(other.End) && other.Start.Before(period.End)
}

// refusal turns a constraint PostgreSQL enforced into the error the admin or visitor sees.
func refusal(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	refused, ok := refusals[pgErr.ConstraintName]
	if !ok {
		return err
	}
	return &refused
}

func unprocessable(field, message string) apperr.Error {
	return apperr.Error{
		Code:   apperr.ActionNotAllowed,
		Detail: "A scheduling rule refused the request.",
		Fields: []apperr.FieldError{{Field: field, Message: message}},
	}
}

func countConflict(key string, err error) error {
	var appErr *apperr.Error
	if errors.As(err, &appErr) && appErr.Code == apperr.SlotUnavailable {
		slotConflicts.Add(key, 1)
	}
	return err
}
