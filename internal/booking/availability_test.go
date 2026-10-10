package booking_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
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

var sydney, _ = time.LoadLocation("Australia/Sydney")

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func local(y int, m time.Month, d, hour int) time.Time {
	return time.Date(y, m, d, hour, 0, 0, 0, sydney)
}

// createOverride removes the override when the test ends, so its fixed date never closes a later test's day.
func createOverride(t *testing.T, o booking.Override) (db.AvailabilityOverride, error) {
	t.Helper()
	row, err := booking.CreateOverride(context.Background(), pgtest.Pool(t), o, practitioner(t))
	if err == nil {
		t.Cleanup(func() {
			_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM availability_overrides WHERE id = $1", row.ID)
			require.NoError(t, err)
		})
	}
	return row, err
}

func TestCreateOverride_OpenOnASunday(t *testing.T) {
	o, err := createOverride(t, booking.Override{
		OnDate: date(2031, 3, 2), Kind: "open", Period: booking.Period{Start: local(2031, 3, 2, 10), End: local(2031, 3, 2, 12)},
		Note: pgtype.Text{String: "Extra session", Valid: true},
	})
	require.NoError(t, err)

	rows, err := booking.ListOverrides(context.Background(), db.New(pgtest.Pool(t)), date(2031, 3, 2), date(2031, 3, 2))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, o.ID, rows[0].ID)
	require.True(t, local(2031, 3, 2, 10).Equal(booking.PeriodOf(rows[0].Period).Start))
}

// An override may end at the next local midnight, but not after it.
func TestCreateOverride_OutsideDateRefused(t *testing.T) {
	_, err := createOverride(t, booking.Override{OnDate: date(2031, 3, 3), Kind: "open",
		Period: booking.Period{Start: local(2031, 3, 3, 20), End: local(2031, 3, 4, 0)}})
	require.NoError(t, err)

	_, err = createOverride(t, booking.Override{OnDate: date(2031, 3, 3), Kind: "open",
		Period: booking.Period{Start: local(2031, 3, 3, 22), End: local(2031, 3, 4, 1)}})
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, []apperr.FieldError{{Field: "/endsAt", Message: "must fall on onDate in the practice timezone"}}, e.Fields)

	_, err = createOverride(t, booking.Override{OnDate: date(2031, 3, 3), Kind: "replace",
		Period: booking.Period{Start: local(2031, 3, 3, 12), End: local(2031, 3, 3, 12)}})
	e = requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/endsAt", e.Fields[0].Field)
}

func TestUpdateOverride(t *testing.T) {
	o, err := createOverride(t, booking.Override{OnDate: date(2031, 3, 5), Kind: "open",
		Period: booking.Period{Start: local(2031, 3, 5, 9), End: local(2031, 3, 5, 10)}})
	require.NoError(t, err)
	changed := booking.Override{OnDate: date(2031, 3, 5), Kind: "replace",
		Period: booking.Period{Start: local(2031, 3, 5, 13), End: local(2031, 3, 5, 15)}}
	updated, err := booking.UpdateOverride(context.Background(), pgtest.Pool(t), o.ID, changed, time.Now())
	require.NoError(t, err)
	require.Equal(t, "replace", updated.Kind)

	require.NoError(t, booking.DeleteOverride(context.Background(), pgtest.Pool(t), o.ID))
	_, err = booking.UpdateOverride(context.Background(), pgtest.Pool(t), o.ID, changed, time.Now())
	requireCode(t, apperr.NotFound, err)
	requireCode(t, apperr.NotFound, booking.DeleteOverride(context.Background(), pgtest.Pool(t), o.ID))
}

func TestDateWindowDefaultsToTheNext62LocalDays(t *testing.T) {
	// 23:30 UTC on 1 March is already 2 March in Sydney.
	first, last, err := booking.DateWindow(time.Date(2031, 3, 1, 23, 30, 0, 0, time.UTC), sydney, nil, nil)
	require.NoError(t, err)
	require.Equal(t, date(2031, 3, 2), first)
	require.Equal(t, date(2031, 5, 3), last)

	from, to := date(2031, 3, 9), date(2031, 3, 8)
	_, _, err = booking.DateWindow(time.Now(), sydney, &from, &to)
	requireCode(t, apperr.InvalidRequest, err)
}

func createBlock(t *testing.T, b booking.Block) (booking.SavedBlock, error) {
	t.Helper()
	saved, err := booking.CreateBlock(context.Background(), pgtest.Pool(t), b, practitioner(t))
	if err == nil {
		deleteBlockAfter(t, saved.Block.ID)
	}
	return saved, err
}

// deleteBlockAfter removes a block when the test ends. A block on a UTC day reaches into the next Sydney
// day, which freeDay hands to the next test.
func deleteBlockAfter(t *testing.T, id pgtype.UUID) {
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM availability_blocks WHERE id = $1", id)
		require.NoError(t, err)
	})
}

func listBlocks(t *testing.T, first, last time.Time) []db.AvailabilityBlock {
	t.Helper()
	rows, err := booking.ListBlocks(context.Background(), db.New(pgtest.Pool(t)), booking.LocalDays(first, last, sydney))
	require.NoError(t, err)
	return rows
}

func TestCreateBlock_TwoWholeDays(t *testing.T) {
	saved, err := createBlock(t, booking.Block{AllDay: true,
		Period: booking.LocalDays(date(2032, 6, 1), date(2032, 6, 2), sydney),
		Reason: pgtype.Text{String: "Conference", Valid: true}})
	require.NoError(t, err)
	require.Empty(t, saved.Conflicts)

	for _, d := range []int{1, 2} {
		rows := listBlocks(t, date(2032, 6, d), date(2032, 6, d))
		require.Len(t, rows, 1)
		require.Equal(t, saved.Block.ID, rows[0].ID)
		require.True(t, rows[0].AllDay)
	}
	require.Empty(t, listBlocks(t, date(2032, 6, 3), date(2032, 6, 3)))
}

// A block overlapping appointments is saved with a warning and
// never cancels, declines or edits them.
func TestCreateBlock_ListsConflicts(t *testing.T) {
	day := freeDay()
	confirmed, err := insert(t, appointment(t, booking.Confirmed, day.Add(9*time.Hour)))
	require.NoError(t, err)
	pending, err := insert(t, appointment(t, booking.Pending, day.Add(11*time.Hour)))
	require.NoError(t, err)
	_, err = insert(t, appointment(t, booking.Declined, day.Add(10*time.Hour+15*time.Minute)))
	require.NoError(t, err)
	before := []string{rowJSON(t, confirmed.ID), rowJSON(t, pending.ID)}

	saved, err := createBlock(t, booking.Block{Period: booking.Period{Start: day.Add(9 * time.Hour), End: day.Add(12 * time.Hour)}})
	require.NoError(t, err)
	var got []pgtype.UUID
	for _, c := range saved.Conflicts {
		got = append(got, c.ID)
	}
	require.Equal(t, []pgtype.UUID{confirmed.ID, pending.ID}, got, "declined rows are no conflict")
	require.Equal(t, before, []string{rowJSON(t, confirmed.ID), rowJSON(t, pending.ID)})
}

func TestBlockFailures(t *testing.T) {
	day := freeDay()
	_, err := createBlock(t, booking.Block{Period: booking.Period{Start: day, End: day}})
	e := requireCode(t, apperr.ActionNotAllowed, err)
	require.Equal(t, "/endsAt", e.Fields[0].Field)

	missing := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	_, err = booking.UpdateBlock(context.Background(), pgtest.Pool(t), missing,
		booking.Block{Period: booking.Period{Start: day, End: day.Add(time.Hour)}}, time.Now())
	requireCode(t, apperr.NotFound, err)
	requireCode(t, apperr.NotFound, booking.DeleteBlock(context.Background(), pgtest.Pool(t), missing))
}

// Sydney's clocks went back on Sunday 5 April 2026: that local day is 25
// hours long, and a whole-day block for it lists on it alone.
func TestBlock_AllDayAcrossDaylightSaving(t *testing.T) {
	period := booking.LocalDays(date(2026, 4, 5), date(2026, 4, 5), sydney)
	require.Equal(t, 25*time.Hour, period.End.Sub(period.Start))
	saved, err := createBlock(t, booking.Block{AllDay: true, Period: period})
	require.NoError(t, err)

	require.Equal(t, []pgtype.UUID{saved.Block.ID}, blockIDs(listBlocks(t, date(2026, 4, 5), date(2026, 4, 5))))
	require.Empty(t, listBlocks(t, date(2026, 4, 4), date(2026, 4, 4)))
	require.Empty(t, listBlocks(t, date(2026, 4, 6), date(2026, 4, 6)))
}

func blockIDs(rows []db.AvailabilityBlock) []pgtype.UUID {
	ids := make([]pgtype.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// A block waits for an appointment being created under the schedule lock,
// then sees it as a conflict: the two never interleave.
func TestBlockWaitsForTheScheduleLock(t *testing.T) {
	ctx := context.Background()
	day := freeDay()
	a := appointment(t, booking.Confirmed, day.Add(9*time.Hour))
	by := practitioner(t)

	tx, err := pgtest.Pool(t).Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)
	require.NoError(t, q.LockSchedule(ctx))
	booked, err := booking.InsertAppointment(ctx, q, testSecret, a)
	require.NoError(t, err)

	type result struct {
		saved booking.SavedBlock
		err   error
	}
	done := make(chan result, 1)
	go func() {
		saved, err := booking.CreateBlock(ctx, pgtest.Pool(t),
			booking.Block{Period: booking.Period{Start: day, End: day.Add(24 * time.Hour)}}, by)
		done <- result{saved, err}
	}()
	select {
	case <-done:
		t.Fatal("the block was saved while an appointment insert held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(ctx))

	r := <-done
	require.NoError(t, r.err)
	deleteBlockAfter(t, r.saved.Block.ID)
	require.Len(t, r.saved.Conflicts, 1)
	require.Equal(t, booked.ID, r.saved.Conflicts[0].ID)
}
