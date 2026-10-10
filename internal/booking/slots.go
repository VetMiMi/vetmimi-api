package booking

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// maxSlotDays is how far after from a request for slots may reach
// (openapi.yaml, "at most 62 days after from").
const maxSlotDays = 62

// SlotDay is the free slots on one local date. Date is that calendar date at
// midnight UTC, as every date in the package is; each slot is the session
// itself, buffers excluded.
type SlotDay struct {
	Date  time.Time
	Slots []Period
}

// Availability is the free slots for one service on local dates First to
// Last, with the practice timezone they are shown in. Days holds only dates
// with at least one slot.
type Availability struct {
	Timezone    string
	First, Last time.Time
	Days        []SlotDay
}

var errNotBookable = apperr.New(apperr.ServiceNotBookable, "The service cannot be booked or requested.")

// Slots is the free slots for svc on local dates first to last, as the
// public site sees them (ADR-004: public availability is slots, not
// appointments). A first date before today is moved to today.
func Slots(ctx context.Context, q db.Querier, svc db.Service, first, last, now time.Time) (Availability, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	return slots(ctx, q, svc, cur, first, last, now)
}

func slots(ctx context.Context, q db.Querier, svc db.Service, cur settings.Settings, first, last, now time.Time) (Availability, error) {
	if err := checkSlotRange(first, last); err != nil {
		return Availability{}, err
	}
	if !bookable(svc) {
		return Availability{}, errNotBookable
	}
	in, err := slotInput(ctx, q, svc, time.Duration(svc.DurationMinutes.Int32)*time.Minute, cur, first, last, now,
		pgtype.UUID{})
	if err != nil {
		return Availability{}, err
	}
	return Availability{Timezone: cur.Timezone, First: in.First, Last: in.Last, Days: FreeSlots(in)}, nil
}

// slotInput reads what FreeSlots needs for a session of duration with svc's
// buffers on local dates first to last. A first date before today is moved
// to today. except, when valid, is an appointment being moved: its own busy
// time does not count against it.
func slotInput(ctx context.Context, q db.Querier, svc db.Service, duration time.Duration, cur settings.Settings,
	first, last, now time.Time, except pgtype.UUID) (SlotInput, error) {
	loc, err := cur.Location()
	if err != nil {
		return SlotInput{}, err
	}
	local := now.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if first.Before(today) && !last.Before(today) {
		first = today
	}

	rules, err := q.ListAvailabilityRules(ctx)
	if err != nil {
		return SlotInput{}, err
	}
	overrides, err := ListOverrides(ctx, q, first, last)
	if err != nil {
		return SlotInput{}, err
	}
	busy, err := q.ListBusyPeriods(ctx, db.ListBusyPeriodsParams{
		Within: LocalDays(first, last, loc).tstzrange(), ExceptID: except})
	if err != nil {
		return SlotInput{}, err
	}
	in := SlotInput{
		Location: loc, First: first, Last: last, Now: now,
		Rules: rules, Overrides: overrides, Busy: make([]Period, len(busy)),
		Duration:       duration,
		BufferBefore:   time.Duration(svc.BufferBeforeMinutes) * time.Minute,
		BufferAfter:    time.Duration(svc.BufferAfterMinutes) * time.Minute,
		Step:           time.Duration(cur.SlotStepMinutes) * time.Minute,
		MinNotice:      time.Duration(cur.MinNoticeHours) * time.Hour,
		MaxAdvanceDays: cur.MaxAdvanceDays,
	}
	for i, b := range busy {
		in.Busy[i] = PeriodOf(b)
	}
	return in, nil
}

// bookable reports whether visitors may book or request svc.
func bookable(svc db.Service) bool {
	return svc.State == "active" && (svc.BookingAction == "book" || svc.BookingAction == "request") &&
		svc.DurationMinutes.Valid
}

func checkSlotRange(first, last time.Time) error {
	if last.Before(first) {
		return apperr.Invalid("to is before from.", apperr.FieldError{Field: "to", Message: "must not be before from"})
	}
	if last.After(first.AddDate(0, 0, maxSlotDays)) {
		return apperr.Invalid("The range is too long.", apperr.FieldError{Field: "to", Message: "must be at most 62 days after from"})
	}
	return nil
}

// SlotInput is everything slot generation reads, so FreeSlots needs no
// database. First and Last are local dates; Busy is every block and every
// pending or confirmed appointment's busy range.
type SlotInput struct {
	Location       *time.Location
	First, Last    time.Time
	Now            time.Time
	Rules          []db.AvailabilityRule
	Overrides      []db.AvailabilityOverride
	Busy           []Period
	Duration       time.Duration
	BufferBefore   time.Duration
	BufferAfter    time.Duration
	Step           time.Duration
	MinNotice      time.Duration
	MaxAdvanceDays int
}

// FreeSlots is the booking requirements' formula: open periods minus busy
// time, buffers and the booking window. Candidates step through each open
// period in real time, so the hour daylight saving skips never yields a
// start; when it repeats an hour, the repeated wall-clock times are kept
// once, at their first instant, so a visitor never sees one time twice.
func FreeSlots(in SlotInput) []SlotDay {
	earliest := in.Now.Add(in.MinNotice)
	local := in.Now.In(in.Location)
	horizon := time.Date(local.Year(), local.Month(), local.Day()+in.MaxAdvanceDays+1, 0, 0, 0, 0, in.Location)

	var days []SlotDay
	for d := in.First; !d.After(in.Last); d = d.AddDate(0, 0, 1) {
		var free []Period
		seen := map[[2]int]bool{}
		for _, open := range in.openPeriods(d) {
			for start := open.Start; ; start = start.Add(in.Step) {
				need := Period{Start: start.Add(-in.BufferBefore), End: start.Add(in.Duration + in.BufferAfter)}
				if need.End.After(open.End) {
					break
				}
				if need.Start.Before(open.Start) || start.Before(earliest) || !start.Before(horizon) ||
					slices.ContainsFunc(in.Busy, need.overlaps) {
					continue
				}
				wall := start.In(in.Location)
				if key := [2]int{wall.Hour(), wall.Minute()}; !seen[key] {
					seen[key] = true
					free = append(free, Period{Start: start, End: start.Add(in.Duration)})
				}
			}
		}
		if len(free) > 0 {
			days = append(days, SlotDay{Date: d, Slots: free})
		}
	}
	return days
}

// openPeriods is when the practice is open on local date d: the weekly rules
// for its weekday, or the date's replace overrides instead when it has any,
// plus its open overrides, merged where they overlap or touch.
func (in SlotInput) openPeriods(d time.Time) []Period {
	var periods []Period
	replaced := false
	for _, o := range in.Overrides {
		if sameDate(o.OnDate.Time, d) {
			periods = append(periods, PeriodOf(o.Period))
			replaced = replaced || o.Kind == "replace"
		}
	}
	if !replaced {
		weekday := int16(d.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		for _, r := range in.Rules {
			if r.Weekday == weekday {
				periods = append(periods, Period{Start: wallClock(d, r.StartTime, in.Location),
					End: wallClock(d, r.EndTime, in.Location)})
			}
		}
	}
	slices.SortFunc(periods, func(a, b Period) int { return a.Start.Compare(b.Start) })
	var merged []Period
	for _, p := range periods {
		if n := len(merged); n > 0 && !p.Start.After(merged[n-1].End) {
			if p.End.After(merged[n-1].End) {
				merged[n-1].End = p.End
			}
			continue
		}
		merged = append(merged, p)
	}
	return merged
}

// wallClock places time column t on local date d with time.Date, which
// applies the date's own offset. A time the clocks skip lands after the gap.
func wallClock(d time.Time, t pgtype.Time, loc *time.Location) time.Time {
	minutes := int(t.Microseconds / int64(time.Minute/time.Microsecond))
	return time.Date(d.Year(), d.Month(), d.Day(), minutes/60, minutes%60, 0, 0, loc)
}

func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

func (p Period) overlaps(o Period) bool {
	return p.Start.Before(o.End) && o.Start.Before(p.End)
}
