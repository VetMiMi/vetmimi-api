package booking

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// refusals are the constraints a client's request can break, each with the
// error the client gets. The constraint is the guard; this only names it.
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

// refusal turns a violation of a constraint in refusals into its error, and
// returns any other error as it is.
func refusal(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if e, ok := refusals[pgErr.ConstraintName]; ok {
			return &e
		}
	}
	return err
}

// unprocessable is a 422 naming the fields a business rule refused.
func unprocessable(field, message string) apperr.Error {
	return apperr.Error{Code: apperr.ActionNotAllowed, Detail: "A scheduling rule refused the request.",
		Fields: []apperr.FieldError{{Field: field, Message: message}}}
}
