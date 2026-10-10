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

// Override adds hours to one local date ("open") or replaces its weekly rules ("replace").
type Override struct {
	OnDate time.Time
	Kind   string
	Period Period
	Note   pgtype.Text
}

// Block is time the practitioner is away; AllDay only records how it was entered.
type Block struct {
	Period Period
	AllDay bool
	Reason pgtype.Text
}

// SavedBlock lists the appointments a block overlaps; they are left for the admin to decide on.
type SavedBlock struct {
	Block     db.AvailabilityBlock
	Conflicts []db.ListOverlappingAppointmentsRow
}

const defaultWindowDays = 62

var (
	errRuleNotFound     = apperr.New(apperr.NotFound, "No availability period has this id.")
	errOverrideNotFound = apperr.New(apperr.NotFound, "No override has this id.")
	errBlockNotFound    = apperr.New(apperr.NotFound, "No block has this id.")
)

func ListRules(ctx context.Context, q db.Querier) ([]db.AvailabilityRule, error) {
	return q.ListAvailabilityRules(ctx)
}

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

func DeleteRule(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	return deleteInSchedule(ctx, pool, errRuleNotFound, func(q *db.Queries) (int64, error) {
		return q.DeleteAvailabilityRule(ctx, id)
	})
}

func (r Rule) times() (pgtype.Time, pgtype.Time, error) {
	start, err := parseClock(r.Start)
	if err != nil {
		return pgtype.Time{}, pgtype.Time{}, err
	}
	end, err := parseClock(r.End)
	return start, end, err
}

func parseClock(s string) (pgtype.Time, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return pgtype.Time{}, apperr.Invalid("Times are HH:MM.", apperr.FieldError{Field: "time", Message: "must be HH:MM"})
	}
	minutes := int64(t.Hour()*60 + t.Minute())
	return pgtype.Time{Microseconds: minutes * int64(time.Minute/time.Microsecond), Valid: true}, nil
}

func Clock(t pgtype.Time) string {
	minutes := t.Microseconds / int64(time.Minute/time.Microsecond)
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

func ListOverrides(ctx context.Context, q db.Querier, first, last time.Time) ([]db.AvailabilityOverride, error) {
	return q.ListAvailabilityOverrides(ctx, db.ListAvailabilityOverridesParams{
		FromDate: pgtype.Date{Time: first, Valid: true}, ToDate: pgtype.Date{Time: last, Valid: true}})
}

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

func DeleteOverride(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	return deleteInSchedule(ctx, pool, errOverrideNotFound, func(q *db.Queries) (int64, error) {
		return q.DeleteAvailabilityOverride(ctx, id)
	})
}

// checkOverride requires a non-empty period inside its local date, ending at the next midnight at the latest.
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

func ListBlocks(ctx context.Context, q db.Querier, within Period) ([]db.AvailabilityBlock, error) {
	return q.ListAvailabilityBlocks(ctx, within.tstzrange())
}

func CreateBlock(ctx context.Context, pool *pgxpool.Pool, b Block, by pgtype.UUID) (SavedBlock, error) {
	if err := checkBlock(b); err != nil {
		return SavedBlock{}, err
	}
	var saved SavedBlock
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		saved.Block, err = q.CreateAvailabilityBlock(ctx, db.CreateAvailabilityBlockParams{
			Period: b.Period.tstzrange(), AllDay: b.AllDay, Reason: b.Reason, CreatedBy: by})
		if err != nil {
			return err
		}
		saved.Conflicts, err = q.ListOverlappingAppointments(ctx, b.Period.tstzrange())
		return err
	})
	return saved, err
}

func UpdateBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, b Block, now time.Time) (SavedBlock, error) {
	if err := checkBlock(b); err != nil {
		return SavedBlock{}, err
	}
	var saved SavedBlock
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		saved.Block, err = q.UpdateAvailabilityBlock(ctx, db.UpdateAvailabilityBlockParams{
			ID: id, Period: b.Period.tstzrange(), AllDay: b.AllDay, Reason: b.Reason, Now: now})
		if err != nil {
			return err
		}
		saved.Conflicts, err = q.ListOverlappingAppointments(ctx, b.Period.tstzrange())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedBlock{}, errBlockNotFound
	}
	return saved, err
}

func DeleteBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	return deleteInSchedule(ctx, pool, errBlockNotFound, func(q *db.Queries) (int64, error) {
		return q.DeleteAvailabilityBlock(ctx, id)
	})
}

func checkBlock(b Block) error {
	if !b.Period.End.After(b.Period.Start) {
		e := unprocessable("/endsAt", "must be later than startsAt")
		return &e
	}
	return nil
}

func deleteInSchedule(ctx context.Context, pool *pgxpool.Pool, notFound error,
	del func(q *db.Queries) (int64, error)) error {
	var n int64
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		n, err = del(q)
		return err
	})
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
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

// localDate is t's date in loc as midnight UTC, the form every date in this package takes.
func localDate(t time.Time, loc *time.Location) time.Time {
	local := t.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
}

func PracticeZone(ctx context.Context, q db.Querier) (string, *time.Location, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return "", nil, err
	}
	loc, err := cur.Location()
	return cur.Timezone, loc, err
}
