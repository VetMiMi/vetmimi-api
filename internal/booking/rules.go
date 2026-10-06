package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// Rule is a weekly availability period: an ISO weekday (Monday = 1) and local
// practice times as HH:MM. It stays wall-clock time; slot generation places it
// on each date in the practice timezone.
type Rule struct {
	Weekday    int16
	Start, End string
}

var errRuleNotFound = apperr.New(apperr.NotFound, "No availability period has this id.")

// ListRules lists every weekly period by weekday, then start time.
func ListRules(ctx context.Context, q db.Querier) ([]db.AvailabilityRule, error) {
	return q.ListAvailabilityRules(ctx)
}

// CreateRule saves a weekly period. One that overlaps another on its weekday
// is refused as overlapping_period by availability_rules_no_overlap.
func CreateRule(ctx context.Context, pool *pgxpool.Pool, r Rule) (db.AvailabilityRule, error) {
	start, end, err := r.times()
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	var row db.AvailabilityRule
	err = inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		row, err = q.CreateAvailabilityRule(ctx, db.CreateAvailabilityRuleParams{
			Weekday: r.Weekday, StartTime: start, EndTime: end})
		return err
	})
	return row, refusal(err)
}

// UpdateRule replaces a weekly period, under the same rules as CreateRule.
func UpdateRule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, r Rule, now time.Time) (db.AvailabilityRule, error) {
	start, end, err := r.times()
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	var row db.AvailabilityRule
	err = inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		row, err = q.UpdateAvailabilityRule(ctx, db.UpdateAvailabilityRuleParams{
			ID: id, Weekday: r.Weekday, StartTime: start, EndTime: end, Now: now})
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AvailabilityRule{}, errRuleNotFound
	}
	return row, refusal(err)
}

// DeleteRule deletes a weekly period.
func DeleteRule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	var n int64
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		n, err = q.DeleteAvailabilityRule(ctx, id)
		return err
	})
	if err == nil && n == 0 {
		return errRuleNotFound
	}
	return err
}

func (r Rule) times() (start, end pgtype.Time, err error) {
	if start, err = parseClock(r.Start); err != nil {
		return
	}
	end, err = parseClock(r.End)
	return
}

// parseClock reads HH:MM, which the contract has already checked.
func parseClock(s string) (pgtype.Time, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return pgtype.Time{}, apperr.Invalid("Times are HH:MM.", apperr.FieldError{Field: "time", Message: "must be HH:MM"})
	}
	minutes := int64(t.Hour()*60 + t.Minute())
	return pgtype.Time{Microseconds: minutes * int64(time.Minute/time.Microsecond), Valid: true}, nil
}

// Clock writes a time column as HH:MM.
func Clock(t pgtype.Time) string {
	minutes := t.Microseconds / int64(time.Minute/time.Microsecond)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}
