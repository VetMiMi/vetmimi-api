package booking

import (
	"context"
	"expvar"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// purgeBatch bounds one DELETE, so the live host's small PostgreSQL is never locked for long.
const purgeBatch = 500

var (
	appointmentsDeleted = expvar.NewInt("appointments_deleted")
	enquiriesDeleted    = expvar.NewInt("enquiries_deleted")
)

type Purged struct {
	Appointments, Enquiries int64
}

// Purge deletes final appointments and contact enquiries older than retention_months.
func Purge(ctx context.Context, q db.Querier, now time.Time) (Purged, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Purged{}, err
	}
	before := now.AddDate(0, -cur.RetentionMonths, 0)
	var out Purged
	out.Appointments, err = inBatches(func() (int64, error) {
		return q.DeleteRetainedAppointments(ctx, db.DeleteRetainedAppointmentsParams{Before: before, MaxRows: purgeBatch})
	})
	appointmentsDeleted.Add(out.Appointments)
	if err != nil {
		return out, err
	}
	out.Enquiries, err = inBatches(func() (int64, error) {
		return q.DeleteRetainedContactEnquiries(ctx, db.DeleteRetainedContactEnquiriesParams{Before: before, MaxRows: purgeBatch})
	})
	enquiriesDeleted.Add(out.Enquiries)
	return out, err
}

func inBatches(del func() (int64, error)) (int64, error) {
	var total int64
	for {
		n, err := del()
		total += n
		if err != nil || n < purgeBatch {
			return total, err
		}
	}
}
