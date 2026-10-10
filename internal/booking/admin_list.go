package booking

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// View is a preset of the admin list. The default puts upcoming and pending
// appointments first rather than the whole history (Booking & Admin UX §10).
type View string

const (
	ViewUpcoming  View = "upcoming"
	ViewPending   View = "pending"
	ViewPast      View = "past"
	ViewCancelled View = "cancelled"
	ViewAll       View = "all"
)

var allStatuses = []Status{Pending, Confirmed, Declined, Expired, CancelledByClient, CancelledByPractitioner,
	Completed, NoShow}

// Filter narrows the admin list; zero fields filter nothing. From and To
// bound starts_at; Search matches the reference, the visitor's name or email.
type Filter struct {
	View      View
	Statuses  []Status
	ServiceID pgtype.UUID
	From, To  time.Time
	Format    string
	Search    string
	Cursor    string
	Limit     int
}

// Page is one page of the list, in the practice timezone's terms. Items
// carry the visitor's name only: overview screens expose minimal
// information (Booking & Admin UX §28). NextCursor is empty on the last page.
type Page struct {
	Timezone   string
	Items      []db.ListAppointmentsRow
	NextCursor string
}

const defaultLimit = 50

// ListAppointments lists appointments for the admin, a page at a time. Each
// view has its own order: upcoming soonest first, pending by the hold that
// ends first, the others newest first.
func ListAppointments(ctx context.Context, q db.Querier, f Filter, now time.Time) (Page, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Page{}, err
	}
	p := db.ListAppointmentsParams{
		StartsFrom:   optionalTime(f.From),
		StartsBefore: optionalTime(f.To),
		ServiceID:    f.ServiceID,
		Format:       pgtype.Text{String: f.Format, Valid: f.Format != ""},
		Search:       listing.LikePattern(f.Search),
		MaxRows:      int32(cmpLimit(f.Limit)) + 1,
	}
	preset := allStatuses
	switch f.View {
	case ViewUpcoming, "":
		preset, p.Ascending = []Status{Pending, Confirmed}, true
		if !p.StartsFrom.Valid || p.StartsFrom.Time.Before(now) {
			p.StartsFrom = sql.NullTime{Time: now, Valid: true}
		}
	case ViewPending:
		preset, p.Ascending, p.ByHold = []Status{Pending}, true, true
	case ViewPast:
		p.Past = sql.NullTime{Time: now, Valid: true}
	case ViewCancelled:
		preset = []Status{CancelledByClient, CancelledByPractitioner, Declined}
	}
	for _, s := range preset {
		if len(f.Statuses) == 0 || slices.Contains(f.Statuses, s) {
			p.Statuses = append(p.Statuses, string(s))
		}
	}
	if f.Cursor != "" {
		if p.AfterAt, p.AfterID, err = listing.DecodeCursor(f.Cursor); err != nil {
			return Page{}, err
		}
	}

	rows, err := q.ListAppointments(ctx, p)
	if err != nil {
		return Page{}, err
	}
	page := Page{Timezone: cur.Timezone, Items: rows}
	if limit := cmpLimit(f.Limit); len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = listing.EncodeCursor(last.SortAt, last.ID)
	}
	return page, nil
}

func cmpLimit(n int) int {
	if n <= 0 {
		return defaultLimit
	}
	return n
}

func optionalTime(t time.Time) sql.NullTime {
	return sql.NullTime{Time: t, Valid: !t.IsZero()}
}

// Detail is everything the admin's appointment screen shows, including its
// history and the messages about it (Booking & Admin UX §12).
type Detail struct {
	Appointment    db.GetAppointmentDetailRow
	Events         []db.ListAppointmentEventsRow
	Communications []db.Communication
	// AllowedActions is what the screen may offer now; each action still
	// re-checks the row.
	AllowedActions []string
	// VideoRoom is the appointment's VetMiMi room, or nil.
	VideoRoom *db.VideoRoom
}

// GetAppointment reads one appointment's detail.
func GetAppointment(ctx context.Context, q db.Querier, id pgtype.UUID, now time.Time) (Detail, error) {
	appt, err := q.GetAppointmentDetail(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Detail{}, errAppointmentNotFound
	}
	if err != nil {
		return Detail{}, err
	}
	events, err := q.ListAppointmentEvents(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	sent, err := q.ListAppointmentCommunications(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Appointment: appt, Events: events, Communications: sent,
		AllowedActions: AllowedActions(Status(appt.Status), appt.StartsAt, now)}
	room, ok, err := video.RoomOf(ctx, q, id)
	if ok {
		d.VideoRoom = &room
		d.AllowedActions = append(d.AllowedActions, video.Actions(room, Status(appt.Status) == Confirmed, now)...)
	}
	return d, err
}

// AllowedActions is what an administrator may do with an appointment in
// status starting at startsAt, as the transition table and the clock allow.
func AllowedActions(status Status, startsAt, now time.Time) []string {
	var out []string
	future := startsAt.After(now)
	switch {
	case status == Pending && future:
		out = []string{"confirm", "decline", "reschedule"}
	case status == Pending:
		out = []string{"decline"}
	case status == Confirmed && future:
		out = []string{"reschedule", "cancel"}
	case status == Confirmed:
		out = []string{"complete", "no_show"}
	}
	return append(out, "set_note", "mark_communicated")
}
