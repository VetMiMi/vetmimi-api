package booking

import (
	"context"
	"expvar"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// TaskPurgeRetention deletes booking data past settings.retention_months
// (docs/architecture.md, "Background jobs").
const TaskPurgeRetention = "booking:purge-retention"

// purgeBatch bounds one DELETE, so the live host's small PostgreSQL is never
// locked for long.
const purgeBatch = 500

var (
	appointmentsDeleted = expvar.NewInt("appointments_deleted")
	enquiriesDeleted    = expvar.NewInt("enquiries_deleted")
)

// Purged is how many rows one purge deleted.
type Purged struct {
	Appointments, Enquiries int64
}

// Purge deletes final appointments that ended, and contact enquiries that
// arrived, more than retention_months calendar months before now, with their
// history and messages (ON DELETE CASCADE). Pending and confirmed
// appointments are kept whatever their age. Running it twice deletes
// nothing the second time.
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

// inBatches runs del until a batch comes back short, and returns the total.
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

func (t *Tasks) purgeRetention(ctx context.Context, _ []byte) error {
	purged, err := Purge(ctx, db.New(t.Pool), t.Now())
	t.Log.InfoContext(ctx, "retention purged",
		"appointments_deleted", purged.Appointments, "enquiries_deleted", purged.Enquiries)
	return err
}
