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

func createBlock(t *testing.T, b booking.Block) (booking.SavedBlock, error) {
	t.Helper()
	return booking.CreateBlock(context.Background(), pgtest.Pool(t), b, practitioner(t))
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

// Section 19: a block overlapping appointments is saved with a warning and
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
	require.Len(t, r.saved.Conflicts, 1)
	require.Equal(t, booked.ID, r.saved.Conflicts[0].ID)
}
