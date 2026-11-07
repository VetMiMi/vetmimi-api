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

type Detail struct {
	Appointment    db.GetAppointmentDetailRow
	Events         []db.ListAppointmentEventsRow
	Communications []db.Communication
	AllowedActions []string
	VideoRoom      *db.VideoRoom
}

const (
	ViewUpcoming  View = "upcoming"
	ViewPending   View = "pending"
	ViewPast      View = "past"
	ViewCancelled View = "cancelled"
	ViewAll       View = "all"
)

const defaultLimit = 50

// maxNoteLength is appointments_visitor_lengths' bound on admin_note.
const maxNoteLength = 2000

var allStatuses = []Status{Pending, Confirmed, Declined, Expired, CancelledByClient, CancelledByPractitioner,
	Completed, NoShow}

func ListAppointments(ctx context.Context, q db.Querier, filter Filter, now time.Time) (Page, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Page{}, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	params, err := listParams(filter, limit, now)
	if err != nil {
		return Page{}, err
	}
	rows, err := q.ListAppointments(ctx, params)
	if err != nil {
		return Page{}, err
	}
	page := Page{
		Timezone: current.Timezone,
		Items:    rows,
	}
	if len(rows) > limit {
		page.Items = rows[:limit]
		last := page.Items[limit-1]
		page.NextCursor = listing.EncodeCursor(last.SortAt, last.ID)
	}
	return page, nil
}

func GetAppointment(ctx context.Context, q db.Querier, id pgtype.UUID, now time.Time) (Detail, error) {
	appointment, err := q.GetAppointmentDetail(ctx, id)
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
	communications, err := q.ListAppointmentCommunications(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	detail := Detail{
		Appointment:    appointment,
		Events:         events,
		Communications: communications,
		AllowedActions: AllowedActions(Status(appointment.Status), appointment.StartsAt, now),
	}
	room, ok, err := video.RoomOf(ctx, q, id)
	if err != nil {
		return Detail{}, err
	}
	if ok {
		detail.VideoRoom = &room
		confirmed := Status(appointment.Status) == Confirmed
		detail.AllowedActions = append(detail.AllowedActions, video.Actions(room, confirmed, now)...)
	}
	return detail, nil
}

func AllowedActions(status Status, startsAt, now time.Time) []string {
	var actions []string
	future := startsAt.After(now)
	switch {
	case status == Pending && future:
		actions = []string{"confirm", "decline", "reschedule"}
	case status == Pending:
		actions = []string{"decline"}
	case status == Confirmed && future:
		actions = []string{"reschedule", "cancel"}
	case status == Confirmed:
		actions = []string{"complete", "no_show"}
	}
	return append(actions, "set_note", "mark_communicated")
}

// SetNote replaces the private note; the history records only its length.
func SetNote(ctx context.Context, pool *pgxpool.Pool, change Change, note string, now time.Time) error {
	length := utf8.RuneCountInString(note)
	if length > maxNoteLength {
		return apperr.Invalid("The note is too long.", apperr.FieldError{Field: "/note", Message: "must be at most 2000 characters"})
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return err
	}
	appointment, err := lockAppointment(ctx, q, change.ID, change.Version)
	if err != nil {
		return err
	}
	params := db.SetAppointmentNoteParams{
		ID:        appointment.ID,
		AdminNote: pgtype.Text{String: note, Valid: note != ""},
		Now:       now,
	}
	_, err = q.SetAppointmentNote(ctx, params)
	if err != nil {
		return err
	}
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "note_updated",
		Actor:         "admin",
		ActorUserID:   change.Actor,
		Detail:        EventDetail{NoteLength: &length},
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

func listParams(filter Filter, limit int, now time.Time) (db.ListAppointmentsParams, error) {
	params := db.ListAppointmentsParams{
		StartsFrom:   sql.NullTime{Time: filter.From, Valid: !filter.From.IsZero()},
		StartsBefore: sql.NullTime{Time: filter.To, Valid: !filter.To.IsZero()},
		ServiceID:    filter.ServiceID,
		Format:       pgtype.Text{String: filter.Format, Valid: filter.Format != ""},
		Search:       listing.LikePattern(filter.Search),
		// One extra row tells whether there is a next page.
		MaxRows: int32(limit) + 1,
	}
	preset := allStatuses
	switch filter.View {
	case ViewUpcoming, "":
		preset = []Status{Pending, Confirmed}
		params.Ascending = true
		if !params.StartsFrom.Valid || params.StartsFrom.Time.Before(now) {
			params.StartsFrom = sql.NullTime{Time: now, Valid: true}
		}
	case ViewPending:
		preset = []Status{Pending}
		params.Ascending = true
		params.ByHold = true
	case ViewPast:
		params.Past = sql.NullTime{Time: now, Valid: true}
	case ViewCancelled:
		preset = []Status{CancelledByClient, CancelledByPractitioner, Declined}
	}
	for _, status := range preset {
		if len(filter.Statuses) == 0 || slices.Contains(filter.Statuses, status) {
			params.Statuses = append(params.Statuses, string(status))
		}
	}
	if filter.Cursor == "" {
		return params, nil
	}
	var err error
	params.AfterAt, params.AfterID, err = listing.DecodeCursor(filter.Cursor)
	if err != nil {
		return db.ListAppointmentsParams{}, err
	}
	return params, nil
}
