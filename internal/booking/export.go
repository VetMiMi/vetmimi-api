package booking

import (
	"context"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Export is the appointments starting on a range of local dates, with the zone their times are shown in.
type Export struct {
	Timezone string
	Location *time.Location
	Rows     []db.ExportAppointmentsRow
}

// maxExportDays keeps a file of clients' personal details to about a year.
const maxExportDays = 366

// ExportAppointments reads every appointment starting on the local dates first to last, in start order.
func ExportAppointments(ctx context.Context, q db.Querier, first, last time.Time) (Export, error) {
	err := checkDateRange(first, last, maxExportDays)
	if err != nil {
		return Export{}, err
	}
	timezone, loc, err := PracticeZone(ctx, q)
	if err != nil {
		return Export{}, err
	}
	days := LocalDays(first, last, loc)
	params := db.ExportAppointmentsParams{
		StartsFrom:   days.Start,
		StartsBefore: days.End,
	}
	rows, err := q.ExportAppointments(ctx, params)
	if err != nil {
		return Export{}, err
	}
	export := Export{
		Timezone: timezone,
		Location: loc,
		Rows:     rows,
	}
	return export, nil
}
