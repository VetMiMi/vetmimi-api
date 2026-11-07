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
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Purged{}, err
	}
	before := now.AddDate(0, -current.RetentionMonths, 0)

	var purged Purged
	purged.Appointments, err = inBatches(func() (int64, error) {
		params := db.DeleteRetainedAppointmentsParams{
			Before:  before,
			MaxRows: purgeBatch,
		}
		return q.DeleteRetainedAppointments(ctx, params)
	})
	appointmentsDeleted.Add(purged.Appointments)
	if err != nil {
		return purged, err
	}
	purged.Enquiries, err = inBatches(func() (int64, error) {
		params := db.DeleteRetainedContactEnquiriesParams{
			Before:  before,
			MaxRows: purgeBatch,
		}
		return q.DeleteRetainedContactEnquiries(ctx, params)
	})
	enquiriesDeleted.Add(purged.Enquiries)
	return purged, err
}

// inBatches repeats deleteBatch until a batch comes back short, so no single DELETE runs long.
func inBatches(deleteBatch func() (int64, error)) (int64, error) {
	var total int64
	for {
		deleted, err := deleteBatch()
		total += deleted
		if err != nil || deleted < purgeBatch {
			return total, err
		}
	}
}
