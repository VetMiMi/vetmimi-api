package booking

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

const (
	maxSlotDays = 62
	// The free slots offered when the requested one is gone.
	maxAlternatives = 5
	alternativeDays = 14
	// anyDay stands in for the furthest bookable day when Daw Mi moves an appointment.
	anyDay = 100 * 365
)

var (
	errNotBookable   = apperr.New(apperr.ServiceNotBookable, "The service cannot be booked or requested.")
	errBookingPaused = apperr.New(apperr.BookingPaused, "Public booking is paused.")
)

// SlotDay's slots are sessions, buffers excluded.
type SlotDay struct {
	Date  time.Time
	Slots []Period
}

type Availability struct {
	Timezone    string
	First, Last time.Time
	Days        []SlotDay
}

// SlotInput is everything FreeSlots reads, so it needs no database.
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

func PublicAvailability(ctx context.Context, q db.Querier, slug string, first, last, now time.Time) (Availability, error) {
	svc, err := q.GetServiceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Availability{}, apperr.New(apperr.NotFound, "No service has this slug.")
	}
	if err != nil {
		return Availability{}, err
	}
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	if !cur.PublicBookingEnabled {
		return Availability{}, errBookingPaused
	}
	return serviceSlots(ctx, q, svc, cur, first, last, now)
}

// Slots is what visitors see, without the public-booking switch.
func Slots(ctx context.Context, q db.Querier, svc db.Service, first, last, now time.Time) (Availability, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	return serviceSlots(ctx, q, svc, cur, first, last, now)
}

func serviceSlots(ctx context.Context, q db.Querier, svc db.Service, cur settings.Settings,
	first, last, now time.Time) (Availability, error) {
	if err := checkSlotRange(first, last); err != nil {
		return Availability{}, err
	}
	if !bookable(svc) {
		return Availability{}, errNotBookable
	}
	duration := time.Duration(svc.DurationMinutes.Int32) * time.Minute
	in, err := slotInput(ctx, q, svc, duration, cur, first, last, now, pgtype.UUID{})
	if err != nil {
		return Availability{}, err
	}
	return Availability{Timezone: cur.Timezone, First: in.First, Last: in.Last, Days: FreeSlots(in)}, nil
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

// slotInput ignores except's own busy time, so an appointment can move within it.
func slotInput(ctx context.Context, q db.Querier, svc db.Service, duration time.Duration, cur settings.Settings,
	first, last, now time.Time, except pgtype.UUID) (SlotInput, error) {
	loc, err := cur.Location()
	if err != nil {
		return SlotInput{}, err
	}
	if today := localDate(now, loc); first.Before(today) && !last.Before(today) {
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

func FreeSlots(in SlotInput) []SlotDay {
	earliest := in.Now.Add(in.MinNotice)
	local := in.Now.In(in.Location)
	horizon := time.Date(local.Year(), local.Month(), local.Day()+in.MaxAdvanceDays+1, 0, 0, 0, 0, in.Location)

	var days []SlotDay
	for d := in.First; !d.After(in.Last); d = d.AddDate(0, 0, 1) {
		if free := in.freeOn(d, earliest, horizon); len(free) > 0 {
			days = append(days, SlotDay{Date: d, Slots: free})
		}
	}
	return days
}

// freeOn steps in real time, so a skipped hour yields no start and a repeated hour is offered once.
func (in SlotInput) freeOn(d, earliest, horizon time.Time) []Period {
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
	return free
}

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
		periods = append(periods, in.weeklyPeriods(d)...)
	}
	return mergePeriods(periods)
}

func (in SlotInput) weeklyPeriods(d time.Time) []Period {
	weekday := int16(d.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	var periods []Period
	for _, r := range in.Rules {
		if r.Weekday == weekday {
			periods = append(periods, Period{Start: wallClock(d, r.StartTime, in.Location),
				End: wallClock(d, r.EndTime, in.Location)})
		}
	}
	return periods
}

func mergePeriods(periods []Period) []Period {
	slices.SortFunc(periods, func(a, b Period) int { return a.Start.Compare(b.Start) })
	var merged []Period
	for _, p := range periods {
		n := len(merged)
		if n == 0 || p.Start.After(merged[n-1].End) {
			merged = append(merged, p)
			continue
		}
		if p.End.After(merged[n-1].End) {
			merged[n-1].End = p.End
		}
	}
	return merged
}

// wallClock uses time.Date, which applies the date's own offset; a skipped time lands after the gap.
func wallClock(d time.Time, t pgtype.Time, loc *time.Location) time.Time {
	minutes := int(t.Microseconds / int64(time.Minute/time.Microsecond))
	return time.Date(d.Year(), d.Month(), d.Day(), minutes/60, minutes%60, 0, 0, loc)
}

func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

type slotRequest struct {
	Service db.Service
	// Zero means the service's duration.
	Duration time.Duration
	Start    time.Time
	// Moving is an appointment being rescheduled; Daw Mi may move it outside the booking window.
	Moving pgtype.UUID
}

// checkSlot requires a free start or offers alternatives; the exclusion constraint still guards against races.
func checkSlot(ctx context.Context, q db.Querier, cur settings.Settings, r slotRequest, now time.Time) error {
	loc, err := cur.Location()
	if err != nil {
		return err
	}
	duration := r.Duration
	if duration == 0 {
		duration = time.Duration(r.Service.DurationMinutes.Int32) * time.Minute
	}
	day := localDate(r.Start, loc)
	in, err := slotInput(ctx, q, r.Service, duration, cur, day, day.AddDate(0, 0, alternativeDays-1), now, r.Moving)
	if err != nil {
		return err
	}
	if r.Moving.Valid {
		in.MinNotice, in.MaxAdvanceDays = 0, anyDay
	}
	var alternatives []Period
	for _, d := range FreeSlots(in) {
		for _, slot := range d.Slots {
			if slot.Start.Equal(r.Start) && sameDate(d.Date, day) {
				return nil
			}
			if len(alternatives) < maxAlternatives {
				alternatives = append(alternatives, slot)
			}
		}
	}
	return slotUnavailable(alternatives)
}

type slotJSON struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

func slotUnavailable(alternatives []Period) error {
	out := make([]slotJSON, len(alternatives))
	for i, p := range alternatives {
		out[i] = slotJSON{StartsAt: p.Start.UTC(), EndsAt: p.End.UTC()}
	}
	return &apperr.Error{Code: apperr.SlotUnavailable, Detail: "The requested time is no longer free.",
		Extensions: map[string]any{"alternatives": out}}
}

// withAlternatives adds the alternatives member the contract requires; the failed transaction cannot read any.
func withAlternatives(err error) error {
	var e *apperr.Error
	if errors.As(err, &e) && e.Code == apperr.SlotUnavailable {
		return slotUnavailable(nil)
	}
	return err
}
