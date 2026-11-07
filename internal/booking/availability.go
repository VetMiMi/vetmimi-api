package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// Rule is a weekly opening period: ISO weekday (Monday = 1) and local times as HH:MM.
type Rule struct {
	Weekday    int16
	Start, End string
}

const defaultWindowDays = 62

var errRuleNotFound = apperr.New(apperr.NotFound, "No availability period has this id.")

func ListRules(ctx context.Context, q db.Querier) ([]db.AvailabilityRule, error) {
	return q.ListAvailabilityRules(ctx)
}

func CreateRule(ctx context.Context, pool *pgxpool.Pool, rule Rule) (db.AvailabilityRule, error) {
	start, end, err := rule.times()
	if err != nil {
		return db.AvailabilityRule{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	params := db.CreateAvailabilityRuleParams{
		Weekday:   rule.Weekday,
		StartTime: start,
		EndTime:   end,
	}
	row, err := q.CreateAvailabilityRule(ctx, params)
	if err != nil {
		return db.AvailabilityRule{}, refusal(err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	return row, nil
}

func UpdateRule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, rule Rule,
	now time.Time) (db.AvailabilityRule, error) {
	start, end, err := rule.times()
	if err != nil {
		return db.AvailabilityRule{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	params := db.UpdateAvailabilityRuleParams{
		ID:        id,
		Weekday:   rule.Weekday,
		StartTime: start,
		EndTime:   end,
		Now:       now,
	}
	row, err := q.UpdateAvailabilityRule(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.AvailabilityRule{}, errRuleNotFound
	}
	if err != nil {
		return db.AvailabilityRule{}, refusal(err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		return db.AvailabilityRule{}, err
	}
	return row, nil
}

func DeleteRule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
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
	deleted, err := q.DeleteAvailabilityRule(ctx, id)
	if err != nil {
		return err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return errRuleNotFound
	}
	return nil
}

// ReplaceRules saves the whole week or nothing; an empty list closes every day.
func ReplaceRules(ctx context.Context, pool *pgxpool.Pool, rules []Rule) ([]db.AvailabilityRule, error) {
	params := make([]db.CreateAvailabilityRuleParams, len(rules))
	for i, rule := range rules {
		start, end, err := rule.times()
		if err != nil {
			return nil, err
		}
		params[i] = db.CreateAvailabilityRuleParams{
			Weekday:   rule.Weekday,
			StartTime: start,
			EndTime:   end,
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return nil, err
	}
	err = q.DeleteAllAvailabilityRules(ctx)
	if err != nil {
		return nil, err
	}
	for _, rule := range params {
		_, err = q.CreateAvailabilityRule(ctx, rule)
		if err != nil {
			return nil, refusal(err)
		}
	}
	rows, err := q.ListAvailabilityRules(ctx)
	if err != nil {
		return nil, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func Clock(t pgtype.Time) string {
	minutes := t.Microseconds / int64(time.Minute/time.Microsecond)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// DateWindow defaults an admin list to today and the next 62 local days.
func DateWindow(now time.Time, loc *time.Location, from, to *time.Time) (time.Time, time.Time, error) {
	first := localDate(now, loc)
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

// LocalDays spans local dates first to last; a day is 23, 24 or 25 hours long.
func LocalDays(first, last time.Time, loc *time.Location) Period {
	return Period{
		Start: time.Date(first.Year(), first.Month(), first.Day(), 0, 0, 0, 0, loc),
		End:   time.Date(last.Year(), last.Month(), last.Day()+1, 0, 0, 0, 0, loc),
	}
}

func PracticeZone(ctx context.Context, q db.Querier) (string, *time.Location, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return "", nil, err
	}
	loc, err := current.Location()
	if err != nil {
		return "", nil, err
	}
	return current.Timezone, loc, nil
}

func (rule Rule) times() (pgtype.Time, pgtype.Time, error) {
	start, err := parseClock(rule.Start)
	if err != nil {
		return pgtype.Time{}, pgtype.Time{}, err
	}
	end, err := parseClock(rule.End)
	if err != nil {
		return pgtype.Time{}, pgtype.Time{}, err
	}
	return start, end, nil
}

func parseClock(s string) (pgtype.Time, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return pgtype.Time{}, apperr.Invalid("Times are HH:MM.", apperr.FieldError{Field: "time", Message: "must be HH:MM"})
	}
	minutes := int64(t.Hour()*60 + t.Minute())
	return pgtype.Time{Microseconds: minutes * int64(time.Minute/time.Microsecond), Valid: true}, nil
}

// localDate is t's date in loc as midnight UTC, the form every date in this package takes.
func localDate(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}
