package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func weekly(weekday int16, start, end string) db.AvailabilityRule {
	clock := func(s string) pgtype.Time {
		t, _ := time.Parse("15:04", s)
		return pgtype.Time{Microseconds: int64(t.Hour()*60+t.Minute()) * 60e6, Valid: true}
	}
	return db.AvailabilityRule{Weekday: weekday, StartTime: clock(start), EndTime: clock(end)}
}

func dateOverride(onDate time.Time, kind string, p booking.Period) db.AvailabilityOverride {
	return db.AvailabilityOverride{OnDate: pgtype.Date{Time: onDate, Valid: true}, Kind: kind,
		Period: pgtype.Range[pgtype.Timestamptz]{
			Lower: pgtype.Timestamptz{Time: p.Start, Valid: true}, Upper: pgtype.Timestamptz{Time: p.End, Valid: true},
			LowerType: pgtype.Inclusive, UpperType: pgtype.Exclusive, Valid: true}}
}

func at(y int, m time.Month, d, hour, minute int) time.Time {
	return time.Date(y, m, d, hour, minute, 0, 0, sydney)
}

// therapyInput is the issue's fixture: a 60-minute service with 15 minutes
// after, a 30-minute step, no notice and a year ahead, on Tuesdays
// 10:00-13:00, seen from Monday 2 November 2026.
func therapyInput() booking.SlotInput {
	return booking.SlotInput{
		Location: sydney, First: date(2026, 11, 8), Last: date(2026, 11, 17), Now: at(2026, 11, 2, 9, 0),
		Rules:    []db.AvailabilityRule{weekly(tuesday, "10:00", "13:00")},
		Duration: time.Hour, BufferAfter: 15 * time.Minute, Step: 30 * time.Minute, MaxAdvanceDays: 365,
	}
}

// starts lists each slot's start as a local time with its offset, checking
// that every slot lasts the service's real duration.
func starts(t *testing.T, in booking.SlotInput) []string {
	t.Helper()
	var out []string
	for _, day := range booking.FreeSlots(in) {
		require.NotEmpty(t, day.Slots, "only days with slots are listed")
		for _, s := range day.Slots {
			require.Equal(t, in.Duration, s.End.Sub(s.Start))
			require.Equal(t, day.Date.Format(time.DateOnly), s.Start.In(sydney).Format(time.DateOnly))
			out = append(out, s.Start.In(sydney).Format("2006-01-02 15:04 -07"))
		}
	}
	return out
}

func TestFreeSlots(t *testing.T) {
	tests := map[string]struct {
		change func(*booking.SlotInput)
		want   []string
	}{
		"the buffer must fit before the period ends": {
			change: func(in *booking.SlotInput) { in.Last = date(2026, 11, 10) },
			want:   []string{"2026-11-10 10:00 +11", "2026-11-10 10:30 +11", "2026-11-10 11:00 +11", "2026-11-10 11:30 +11"},
		},
		"busy time and both buffers remove slots": {
			change: func(in *booking.SlotInput) {
				in.Last = date(2026, 11, 10)
				in.Rules = []db.AvailabilityRule{weekly(tuesday, "09:00", "13:00")}
				// A booked 11:00 session with its 15 minutes after.
				in.Busy = []booking.Period{{Start: at(2026, 11, 10, 11, 0), End: at(2026, 11, 10, 12, 15)}}
			},
			want: []string{"2026-11-10 09:00 +11", "2026-11-10 09:30 +11"},
		},
		"the buffer before must fit after the period starts": {
			change: func(in *booking.SlotInput) {
				in.Last = date(2026, 11, 10)
				in.Rules = []db.AvailabilityRule{weekly(tuesday, "10:00", "12:00")}
				in.BufferBefore, in.BufferAfter = 15*time.Minute, 0
			},
			want: []string{"2026-11-10 10:30 +11", "2026-11-10 11:00 +11"},
		},
		"a replace override changes one Tuesday only": {
			change: func(in *booking.SlotInput) {
				in.Overrides = []db.AvailabilityOverride{dateOverride(date(2026, 11, 10), "replace",
					booking.Period{Start: at(2026, 11, 10, 15, 0), End: at(2026, 11, 10, 16, 15)})}
			},
			want: []string{"2026-11-10 15:00 +11",
				"2026-11-17 10:00 +11", "2026-11-17 10:30 +11", "2026-11-17 11:00 +11", "2026-11-17 11:30 +11"},
		},
		"an open override adds a Sunday, merged with the hours it touches": {
			change: func(in *booking.SlotInput) {
				in.Last = date(2026, 11, 9)
				in.Overrides = []db.AvailabilityOverride{
					dateOverride(date(2026, 11, 8), "open", booking.Period{Start: at(2026, 11, 8, 10, 0), End: at(2026, 11, 8, 10, 45)}),
					dateOverride(date(2026, 11, 8), "open", booking.Period{Start: at(2026, 11, 8, 10, 45), End: at(2026, 11, 8, 11, 45)}),
				}
			},
			want: []string{"2026-11-08 10:00 +11", "2026-11-08 10:30 +11"},
		},
		"a block removes the slots it touches": {
			change: func(in *booking.SlotInput) {
				in.Last = date(2026, 11, 10)
				in.Busy = []booking.Period{{Start: at(2026, 11, 10, 9, 0), End: at(2026, 11, 10, 11, 0)}}
			},
			want: []string{"2026-11-10 11:00 +11", "2026-11-10 11:30 +11"},
		},
		"minimum notice hides tomorrow morning": {
			change: func(in *booking.SlotInput) {
				in.Last = date(2026, 11, 10)
				in.Now = at(2026, 11, 9, 10, 45)
				in.MinNotice = 24 * time.Hour
			},
			want: []string{"2026-11-10 11:00 +11", "2026-11-10 11:30 +11"},
		},
		"nothing after the last day of the advance window": {
			change: func(in *booking.SlotInput) {
				// Today is Tuesday 3 November; the window ends with 10 November.
				in.Now = at(2026, 11, 3, 8, 0)
				in.MaxAdvanceDays = 7
			},
			want: []string{"2026-11-10 10:00 +11", "2026-11-10 10:30 +11", "2026-11-10 11:00 +11", "2026-11-10 11:30 +11"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			in := therapyInput()
			tc.change(&in)
			require.Equal(t, tc.want, starts(t, in))
		})
	}
}

// Slots reads the schedule from the database: a pending request holds its
// time as a confirmed appointment does, and an expired one gives it back.
func TestSlots_PendingHoldRemovesSlot(t *testing.T) {
	clearRules(t)
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	_, err := createRule(t, tuesday, "09:00", "13:00")
	require.NoError(t, err)
	day := date(2031, 6, 3)
	now := at(2031, 5, 20, 9, 0)
	free := func() []string {
		a, err := booking.Slots(ctx, q, therapy(t), day, day, now)
		require.NoError(t, err)
		require.Equal(t, "Australia/Sydney", a.Timezone)
		var out []string
		for _, d := range a.Days {
			for _, s := range d.Slots {
				out = append(out, s.Start.In(sydney).Format("15:04"))
			}
		}
		return out
	}
	require.Equal(t, []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}, free())

	held, err := insert(t, appointment(t, booking.Pending, at(2031, 6, 3, 11, 0)))
	require.NoError(t, err)
	require.Equal(t, []string{"09:00", "09:30"}, free())

	_, err = pgtest.Pool(t).Exec(ctx, "UPDATE appointments SET status = 'expired' WHERE id = $1", held.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"09:00", "09:30", "10:00", "10:30", "11:00", "11:30"}, free())

	block, err := createBlock(t, booking.Block{Period: booking.Period{Start: at(2031, 6, 3, 9, 0), End: at(2031, 6, 3, 10, 0)},
		Reason: pgtype.Text{String: "Dentist", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, booking.DeleteBlock(ctx, pgtest.Pool(t), block.Block.ID)) })
	require.Equal(t, []string{"10:00", "10:30", "11:00", "11:30"}, free())
}

func TestSlots_Refusals(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	now := at(2031, 5, 20, 9, 0)

	paused, err := q.GetServiceBySlug(ctx, "free-consultation")
	require.NoError(t, err)
	_, err = booking.Slots(ctx, q, paused, date(2031, 6, 1), date(2031, 6, 2), now)
	requireCode(t, apperr.ServiceNotBookable, err)
	enquiry, err := q.GetServiceBySlug(ctx, "group-art-wellbeing")
	require.NoError(t, err)
	_, err = booking.Slots(ctx, q, enquiry, date(2031, 6, 1), date(2031, 6, 2), now)
	requireCode(t, apperr.ServiceNotBookable, err)

	_, err = booking.Slots(ctx, q, therapy(t), date(2031, 6, 1), date(2031, 8, 3), now)
	e := requireCode(t, apperr.InvalidRequest, err)
	require.Equal(t, "to", e.Fields[0].Field)
	_, err = booking.Slots(ctx, q, therapy(t), date(2031, 6, 2), date(2031, 6, 1), now)
	requireCode(t, apperr.InvalidRequest, err)
	_, err = booking.Slots(ctx, q, therapy(t), date(2031, 6, 1), date(2031, 8, 2), now)
	require.NoError(t, err, "62 days after from is allowed")
}

// 62 days of 20 rules against 200 appointments is far more than the practice
// will ever hold; the bound is generous so a slow CI runner never fails it.
func TestFreeSlots_LargeScheduleIsFast(t *testing.T) {
	in := therapyInput()
	in.First, in.Last = date(2026, 11, 3), date(2027, 1, 3)
	in.Rules = nil
	for wd := int16(1); wd <= 7; wd++ {
		in.Rules = append(in.Rules, weekly(wd, "07:00", "12:00"), weekly(wd, "13:00", "18:00"), weekly(wd, "19:00", "21:00"))
	}
	in.Rules = in.Rules[:20]
	for i := range 200 {
		start := at(2026, 11, 3, 8, 0).Add(time.Duration(i) * 7 * time.Hour)
		in.Busy = append(in.Busy, booking.Period{Start: start, End: start.Add(75 * time.Minute)})
	}
	began := time.Now()
	require.NotEmpty(t, booking.FreeSlots(in))
	require.Less(t, time.Since(began), 50*time.Millisecond)
}
