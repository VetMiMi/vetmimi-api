package booking

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
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

var errCursor = apperr.Invalid("The cursor is not one this list returned.",
	apperr.FieldError{Field: "cursor", Message: "is malformed"})

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
		Search:       likePattern(f.Search),
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
		if p.AfterAt, p.AfterID, err = decodeCursor(f.Cursor); err != nil {
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
		page.NextCursor = encodeCursor(last.SortAt, last.ID)
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

// likePattern matches s anywhere, with ILIKE's wildcards in s taken
// literally.
func likePattern(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return pgtype.Text{String: "%" + s + "%", Valid: true}
}

// A cursor is the last row's sort time and id, opaque to the client.
func encodeCursor(at time.Time, id pgtype.UUID) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d|%s", at.UnixMicro(), id.String()))
}

func decodeCursor(c string) (sql.NullTime, pgtype.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	var micros int64
	var id pgtype.UUID
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok || id.Scan(rest) != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	if _, err := fmt.Sscan(at, &micros); err != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	return sql.NullTime{Time: time.UnixMicro(micros).UTC(), Valid: true}, id, nil
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
	return Detail{Appointment: appt, Events: events, Communications: sent,
		AllowedActions: AllowedActions(Status(appt.Status), appt.StartsAt, now)}, nil
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
