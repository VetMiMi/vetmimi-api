package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

var sydney, _ = time.LoadLocation("Australia/Sydney")

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func local(y int, m time.Month, d, hour int) time.Time {
	return time.Date(y, m, d, hour, 0, 0, 0, sydney)
}

func createOverride(t *testing.T, o booking.Override) (db.AvailabilityOverride, error) {
	t.Helper()
	return booking.CreateOverride(context.Background(), pgtest.Pool(t), o, practitioner(t))
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
