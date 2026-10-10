package booking

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// holdWarning is how close a hold's end must be to count as expiring.
const holdWarning = 12 * time.Hour

const (
	dashboardListLimit = 20
	todayLimit         = 50
	upcomingDays       = 14
)

// DashboardData is the admin's operational overview, in the practice timezone.
type DashboardData struct {
	Timezone  string
	Counts    db.DashboardCountsRow
	Attention []AttentionItem
	Pending   []db.ListAppointmentsRow
	Today     []db.ListAppointmentsRow
	Upcoming  []db.ListAppointmentsRow
}

// AttentionItem is one thing waiting on Daw Mi; Detail never names the visitor.
type AttentionItem struct {
	Kind          string
	AppointmentID pgtype.UUID
	Reference     string
	StartsAt      time.Time
	Detail        string
}

func Dashboard(ctx context.Context, q db.Querier, now time.Time) (DashboardData, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return DashboardData{}, err
	}
	loc, err := cur.Location()
	if err != nil {
		return DashboardData{}, err
	}
	today := LocalDays(now.In(loc), now.In(loc), loc)
	week := weekOf(now, loc)
	upcoming := Period{Start: today.End, End: today.End.In(loc).AddDate(0, 0, upcomingDays)}

	out := DashboardData{Timezone: cur.Timezone}
	if out.Counts, err = q.DashboardCounts(ctx, db.DashboardCountsParams{Now: now, DayStart: today.Start,
		DayEnd: today.End, WeekStart: week.Start, WeekEnd: week.End}); err != nil {
		return DashboardData{}, err
	}
	if out.Attention, err = attention(ctx, q, now); err != nil {
		return DashboardData{}, err
	}
	if out.Pending, err = q.ListAppointments(ctx, db.ListAppointmentsParams{Statuses: []string{string(Pending)},
		ByHold: true, Ascending: true, MaxRows: dashboardListLimit}); err != nil {
		return DashboardData{}, err
	}
	if out.Today, err = activeIn(ctx, q, today, todayLimit); err != nil {
		return DashboardData{}, err
	}
	out.Upcoming, err = activeIn(ctx, q, upcoming, dashboardListLimit)
	return out, err
}

// weekOf is Monday to Monday in local dates, so a daylight-saving week keeps its 167 or 169 hours.
func weekOf(now time.Time, loc *time.Location) Period {
	local := now.In(loc)
	sinceMonday := (int(local.Weekday()) + 6) % 7
	monday := time.Date(local.Year(), local.Month(), local.Day()-sinceMonday, 0, 0, 0, 0, loc)
	return Period{Start: monday, End: monday.AddDate(0, 0, 7)}
}

func activeIn(ctx context.Context, q db.Querier, p Period, limit int32) ([]db.ListAppointmentsRow, error) {
	return q.ListAppointments(ctx, db.ListAppointmentsParams{
		Statuses:     []string{string(Pending), string(Confirmed)},
		StartsFrom:   sql.NullTime{Time: p.Start, Valid: true},
		StartsBefore: sql.NullTime{Time: p.End, Valid: true},
		Ascending:    true,
		MaxRows:      limit,
	})
}

func attention(ctx context.Context, q db.Querier, now time.Time) ([]AttentionItem, error) {
	rows, err := q.ListAttention(ctx, db.ListAttentionParams{Now: now, HoldHorizon: now.Add(holdWarning)})
	if err != nil {
		return nil, err
	}
	items := make([]AttentionItem, len(rows))
	for i, r := range rows {
		items[i] = AttentionItem{Kind: r.Kind, AppointmentID: r.ID, Reference: r.Reference, StartsAt: r.StartsAt,
			Detail: attentionDetail(r, now)}
	}
	return items, nil
}

func attentionDetail(r db.ListAttentionRow, now time.Time) string {
	switch r.Kind {
	case "pending_request":
		return "Request waiting for a decision"
	case "hold_expiring":
		return holdDetail(r.HoldExpiresAt.Time.Sub(now))
	case "failed_communication":
		return "Email failed: " + strings.ReplaceAll(r.CommunicationKind, "_", " ")
	case "reschedule_requested":
		return "Visitor asked to reschedule"
	case "completion_due":
		return "Mark as completed or no-show"
	case "block_conflict":
		return "Overlaps a blocked time"
	}
	return ""
}

func holdDetail(left time.Duration) string {
	if left <= 0 {
		return "Hold has ended"
	}
	return fmt.Sprintf("Hold ends in %d h", int(math.Ceil(left.Hours())))
}
