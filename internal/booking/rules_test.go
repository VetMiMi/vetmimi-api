package booking_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

const tuesday, sunday = 2, 7

// clearRules empties availability_rules before and after a test, since
// every test in the package shares one database.
func clearRules(t *testing.T) {
	t.Helper()
	empty := func() {
		_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM availability_rules")
		require.NoError(t, err)
	}
	empty()
	t.Cleanup(empty)
}

func createRule(t *testing.T, weekday int16, start, end string) (db.AvailabilityRule, error) {
	t.Helper()
	return booking.CreateRule(context.Background(), pgtest.Pool(t), booking.Rule{Weekday: weekday, Start: start, End: end})
}

func TestCreateRule_TouchingPeriodsSave(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, tuesday, "14:00", "17:00")
	require.NoError(t, err)
	_, err = createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	_, err = createRule(t, tuesday, "13:00", "14:00")
	require.NoError(t, err, "periods may touch")

	rules, err := booking.ListRules(context.Background(), db.New(pgtest.Pool(t)))
	require.NoError(t, err)
	var got []string
	for _, r := range rules {
		got = append(got, booking.Clock(r.StartTime)+"-"+booking.Clock(r.EndTime))
	}
	require.Equal(t, []string{"10:00-13:00", "13:00-14:00", "14:00-17:00"}, got)
}

func TestCreateRule_OverlapRefused(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	_, err = createRule(t, tuesday, "12:30", "15:00")
	requireCode(t, apperr.OverlappingPeriod, err)
	_, err = createRule(t, tuesday+1, "12:30", "15:00")
	require.NoError(t, err, "another weekday is free")
}

func TestCreateRule_EndMustFollowStart(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, tuesday, "13:00", "13:00")
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/endTime", e.Fields[0].Field)
}

func TestUpdateRule(t *testing.T) {
	clearRules(t)
	r, err := createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	moved, err := booking.UpdateRule(context.Background(), pgtest.Pool(t), r.ID,
		booking.Rule{Weekday: sunday, Start: "09:30", End: "11:00"}, time.Now())
	require.NoError(t, err)
	require.Equal(t, int16(sunday), moved.Weekday)
	require.Equal(t, "09:30", booking.Clock(moved.StartTime))

	require.NoError(t, booking.DeleteRule(context.Background(), pgtest.Pool(t), r.ID))
	requireCode(t, apperr.NotFound, booking.DeleteRule(context.Background(), pgtest.Pool(t), r.ID))
	_, err = booking.UpdateRule(context.Background(), pgtest.Pool(t), r.ID,
		booking.Rule{Weekday: sunday, Start: "09:30", End: "11:00"}, time.Now())
	requireCode(t, apperr.NotFound, err)
}

// The exclusion constraint decides between two overlapping periods saved at
// once, not a check in Go.
func TestCreateRule_ConcurrentOverlap(t *testing.T) {
	for range 20 {
		clearRules(t)
		errs := make([]error, 2)
		periods := [2][2]string{{"10:00", "13:00"}, {"12:00", "15:00"}}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range errs {
			wg.Go(func() {
				<-start
				_, errs[i] = booking.CreateRule(context.Background(), pgtest.Pool(t),
					booking.Rule{Weekday: tuesday, Start: periods[i][0], End: periods[i][1]})
			})
		}
		close(start)
		wg.Wait()
		failed := 0
		for _, err := range errs {
			if err != nil {
				requireCode(t, apperr.OverlappingPeriod, err)
				failed++
			}
		}
		require.Equal(t, 1, failed)
	}
}

// A rule is wall-clock time. Saved before either 2026 Sydney change, a Sunday
// 01:00-04:00 reads back unchanged: nothing converted it to UTC.
func TestRule_StoresWallClock(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, sunday, "01:00", "04:00")
	require.NoError(t, err)
	var start, end string
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT start_time::text, end_time::text FROM availability_rules").Scan(&start, &end))
	require.Equal(t, []string{"01:00:00", "04:00:00"}, []string{start, end})
}
