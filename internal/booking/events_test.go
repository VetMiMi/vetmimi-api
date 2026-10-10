package booking_test

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func TestAppendEventRecordsTheChange(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	day := freeDay()
	a, err := insert(t, appointment(t, booking.Confirmed, day.Add(9*time.Hour)))
	require.NoError(t, err)

	late := true
	require.NoError(t, booking.AppendEvent(ctx, db.New(pool), booking.Event{
		AppointmentID: a.ID,
		Kind:          "rescheduled",
		Previous:      &booking.Period{Start: day.Add(9 * time.Hour), End: day.Add(10 * time.Hour)},
		New:           &booking.Period{Start: day.Add(11 * time.Hour), End: day.Add(12 * time.Hour)},
		Actor:         "admin",
		ActorUserID:   practitioner(t),
		Detail:        booking.EventDetail{LateCancellation: &late},
	}))

	var kind string
	var from pgtype.Text
	var previous pgtype.Range[pgtype.Timestamptz]
	var detail []byte
	require.NoError(t, pool.QueryRow(ctx,
		"SELECT kind, from_status, previous_range, detail FROM appointment_events WHERE appointment_id = $1",
		a.ID).Scan(&kind, &from, &previous, &detail))
	require.Equal(t, "rescheduled", kind)
	require.False(t, from.Valid, "no status change, no from_status")
	require.Equal(t, day.Add(9*time.Hour), booking.PeriodOf(previous).Start.UTC())
	require.JSONEq(t, `{"late_cancellation": true}`, string(detail))
}

// Event detail is a fixed set of non-personal facts. A new field fails this
// test until someone has decided it holds no name, email or note.
func TestEventDetailHoldsNoPersonalData(t *testing.T) {
	var keys []string
	for f := range reflect.TypeFor[booking.EventDetail]().Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		keys = append(keys, name)
	}
	require.Equal(t, []string{"late_cancellation", "by", "source", "length", "preferred"}, keys)
}
