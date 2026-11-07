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

type slotRequest struct {
	Service db.Service
	// Zero means the service's duration.
	Duration time.Duration
	Start    time.Time
	// Moving is an appointment being rescheduled; Daw Mi may move it outside the booking window.
	Moving pgtype.UUID
}

type slotJSON struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}

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

func PublicAvailability(ctx context.Context, q db.Querier, slug string, first, last, now time.Time) (Availability, error) {
	service, err := q.GetServiceBySlug(ctx, slug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Availability{}, apperr.New(apperr.NotFound, "No service has this slug.")
	}
	if err != nil {
		return Availability{}, err
	}
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	if !current.PublicBookingEnabled {
		return Availability{}, errBookingPaused
	}
	return serviceSlots(ctx, q, service, current, first, last, now)
}

// Slots is what visitors see, without the public-booking switch.
func Slots(ctx context.Context, q db.Querier, service db.Service, first, last, now time.Time) (Availability, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Availability{}, err
	}
	return serviceSlots(ctx, q, service, current, first, last, now)
}

func FreeSlots(input SlotInput) []SlotDay {
	earliest := input.Now.Add(input.MinNotice)
	local := input.Now.In(input.Location)
	horizon := time.Date(local.Year(), local.Month(), local.Day()+input.MaxAdvanceDays+1, 0, 0, 0, 0, input.Location)

	var days []SlotDay
	for day := input.First; !day.After(input.Last); day = day.AddDate(0, 0, 1) {
		free := input.freeOn(day, earliest, horizon)
		if len(free) > 0 {
			days = append(days, SlotDay{Date: day, Slots: free})
		}
	}
	return days
}

func serviceSlots(ctx context.Context, q db.Querier, service db.Service, current settings.Settings,
	first, last, now time.Time) (Availability, error) {
	err := checkSlotRange(first, last)
	if err != nil {
		return Availability{}, err
	}
	if !bookable(service) {
		return Availability{}, errNotBookable
	}
	duration := time.Duration(service.DurationMinutes.Int32) * time.Minute
	input, err := slotInput(ctx, q, service, duration, current, first, last, now, pgtype.UUID{})
	if err != nil {
		return Availability{}, err
	}
	availability := Availability{
		Timezone: current.Timezone,
		First:    input.First,
		Last:     input.Last,
		Days:     FreeSlots(input),
	}
	return availability, nil
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
func slotInput(ctx context.Context, q db.Querier, service db.Service, duration time.Duration, current settings.Settings,
	first, last, now time.Time, except pgtype.UUID) (SlotInput, error) {
	loc, err := current.Location()
	if err != nil {
		return SlotInput{}, err
	}
	today := localDate(now, loc)
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
	params := db.ListBusyPeriodsParams{
		Within:   LocalDays(first, last, loc).tstzrange(),
		ExceptID: except,
	}
	busy, err := q.ListBusyPeriods(ctx, params)
	if err != nil {
		return SlotInput{}, err
	}
	input := SlotInput{
		Location:       loc,
		First:          first,
		Last:           last,
		Now:            now,
		Rules:          rules,
		Overrides:      overrides,
		Busy:           make([]Period, len(busy)),
		Duration:       duration,
		BufferBefore:   time.Duration(service.BufferBeforeMinutes) * time.Minute,
		BufferAfter:    time.Duration(service.BufferAfterMinutes) * time.Minute,
		Step:           time.Duration(current.SlotStepMinutes) * time.Minute,
		MinNotice:      time.Duration(current.MinNoticeHours) * time.Hour,
		MaxAdvanceDays: current.MaxAdvanceDays,
	}
	for i, period := range busy {
		input.Busy[i] = PeriodOf(period)
	}
	return input, nil
}

// freeOn steps in real time, so a skipped hour yields no start and a repeated hour is offered once.
func (input SlotInput) freeOn(day, earliest, horizon time.Time) []Period {
	var free []Period
	seen := map[[2]int]bool{}
	for _, open := range input.openPeriods(day) {
		for start := open.Start; ; start = start.Add(input.Step) {
			need := Period{Start: start.Add(-input.BufferBefore), End: start.Add(input.Duration + input.BufferAfter)}
			if need.End.After(open.End) {
				break
			}
			if need.Start.Before(open.Start) || start.Before(earliest) || !start.Before(horizon) ||
				slices.ContainsFunc(input.Busy, need.overlaps) {
				continue
			}
			wall := start.In(input.Location)
			key := [2]int{wall.Hour(), wall.Minute()}
			if !seen[key] {
				seen[key] = true
				free = append(free, Period{Start: start, End: start.Add(input.Duration)})
			}
		}
	}
	return free
}

func (input SlotInput) openPeriods(day time.Time) []Period {
	var periods []Period
	replaced := false
	for _, override := range input.Overrides {
		if sameDate(override.OnDate.Time, day) {
			periods = append(periods, PeriodOf(override.Period))
			replaced = replaced || override.Kind == "replace"
		}
	}
	if !replaced {
		periods = append(periods, input.weeklyPeriods(day)...)
	}
	return mergePeriods(periods)
}

func (input SlotInput) weeklyPeriods(day time.Time) []Period {
	weekday := int16(day.Weekday())
	if weekday == 0 {
		weekday = 7
	}
	var periods []Period
	for _, rule := range input.Rules {
		if rule.Weekday == weekday {
			period := Period{
				Start: wallClock(day, rule.StartTime, input.Location),
				End:   wallClock(day, rule.EndTime, input.Location),
			}
			periods = append(periods, period)
		}
	}
	return periods
}

func mergePeriods(periods []Period) []Period {
	slices.SortFunc(periods, func(a, b Period) int { return a.Start.Compare(b.Start) })
	var merged []Period
	for _, period := range periods {
		last := len(merged) - 1
		if last < 0 || period.Start.After(merged[last].End) {
			merged = append(merged, period)
			continue
		}
		if period.End.After(merged[last].End) {
			merged[last].End = period.End
		}
	}
	return merged
}

// wallClock uses time.Date, which applies the date's own offset; a skipped time lands after the gap.
func wallClock(day time.Time, t pgtype.Time, loc *time.Location) time.Time {
	minutes := int(t.Microseconds / int64(time.Minute/time.Microsecond))
	return time.Date(day.Year(), day.Month(), day.Day(), minutes/60, minutes%60, 0, 0, loc)
}

func sameDate(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// checkSlot requires a free start or offers alternatives; the exclusion constraint still guards against races.
func checkSlot(ctx context.Context, q db.Querier, current settings.Settings, request slotRequest, now time.Time) error {
	loc, err := current.Location()
	if err != nil {
		return err
	}
	duration := request.Duration
	if duration == 0 {
		duration = time.Duration(request.Service.DurationMinutes.Int32) * time.Minute
	}
	day := localDate(request.Start, loc)
	lastDay := day.AddDate(0, 0, alternativeDays-1)
	input, err := slotInput(ctx, q, request.Service, duration, current, day, lastDay, now, request.Moving)
	if err != nil {
		return err
	}
	if request.Moving.Valid {
		input.MinNotice = 0
		input.MaxAdvanceDays = anyDay
	}
	var alternatives []Period
	for _, free := range FreeSlots(input) {
		for _, slot := range free.Slots {
			if slot.Start.Equal(request.Start) && sameDate(free.Date, day) {
				return nil
			}
			if len(alternatives) < maxAlternatives {
				alternatives = append(alternatives, slot)
			}
		}
	}
	return slotUnavailable(alternatives)
}

func slotUnavailable(alternatives []Period) error {
	slots := make([]slotJSON, len(alternatives))
	for i, period := range alternatives {
		slots[i] = slotJSON{StartsAt: period.Start.UTC(), EndsAt: period.End.UTC()}
	}
	return &apperr.Error{
		Code:       apperr.SlotUnavailable,
		Detail:     "The requested time is no longer free.",
		Extensions: map[string]any{"alternatives": slots},
	}
}

// withAlternatives adds the alternatives member the contract requires; the failed transaction cannot read any.
func withAlternatives(err error) error {
	var appErr *apperr.Error
	if errors.As(err, &appErr) && appErr.Code == apperr.SlotUnavailable {
		return slotUnavailable(nil)
	}
	return err
}
