package booking

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/listing"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

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

// Filter narrows the admin list; zero fields filter nothing.
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

// Page is one page of the admin list; NextCursor is empty on the last page.
type Page struct {
	Timezone   string
	Items      []db.ListAppointmentsRow
	NextCursor string
}

const defaultLimit = 50

// maxNoteLength is appointments_visitor_lengths' bound on admin_note.
const maxNoteLength = 2000

func ListAppointments(ctx context.Context, q db.Querier, f Filter, now time.Time) (Page, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Page{}, err
	}
	limit := f.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	p, err := listParams(f, limit, now)
	if err != nil {
		return Page{}, err
	}
	rows, err := q.ListAppointments(ctx, p)
	if err != nil {
		return Page{}, err
	}
	page := Page{Timezone: cur.Timezone, Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = listing.EncodeCursor(last.SortAt, last.ID)
	}
	return page, nil
}

func listParams(f Filter, limit int, now time.Time) (db.ListAppointmentsParams, error) {
	p := db.ListAppointmentsParams{
		StartsFrom:   sql.NullTime{Time: f.From, Valid: !f.From.IsZero()},
		StartsBefore: sql.NullTime{Time: f.To, Valid: !f.To.IsZero()},
		ServiceID:    f.ServiceID,
		Format:       pgtype.Text{String: f.Format, Valid: f.Format != ""},
		Search:       listing.LikePattern(f.Search),
		// One extra row tells whether there is a next page.
		MaxRows: int32(limit) + 1,
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
	if f.Cursor == "" {
		return p, nil
	}
	var err error
	p.AfterAt, p.AfterID, err = listing.DecodeCursor(f.Cursor)
	return p, err
}

type Detail struct {
	Appointment    db.GetAppointmentDetailRow
	Events         []db.ListAppointmentEventsRow
	Communications []db.Communication
	AllowedActions []string
	VideoRoom      *db.VideoRoom
}

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

// SetNote replaces the private note; the history records only its length.
func SetNote(ctx context.Context, pool *pgxpool.Pool, c Change, note string, now time.Time) error {
	n := utf8.RuneCountInString(note)
	if n > maxNoteLength {
		return apperr.Invalid("The note is too long.", apperr.FieldError{Field: "/note", Message: "must be at most 2000 characters"})
	}
	_, err := change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		if _, err := q.SetAppointmentNote(ctx, db.SetAppointmentNoteParams{
			ID: appt.ID, AdminNote: pgtype.Text{String: note, Valid: note != ""}, Now: now,
		}); err != nil {
			return Changed{}, err
		}
		return Changed{}, AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "note_updated", Actor: "admin",
			ActorUserID: c.Actor, Detail: EventDetail{NoteLength: &n}})
	})
	return err
}
