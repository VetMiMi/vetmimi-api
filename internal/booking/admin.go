package booking

import (
	"context"
	"errors"
	"expvar"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// Counters on the metrics listener (docs/architecture.md, "Observability"),
// keyed by the path that created or was refused: website, manual or
// reschedule.
var (
	appointmentsCreated = expvar.NewMap("appointments_created")
	slotConflicts       = expvar.NewMap("slot_conflicts")
)

// countConflict counts err under key when it is a slot_unavailable, and
// returns it as it is.
func countConflict(key string, err error) error {
	var e *apperr.Error
	if errors.As(err, &e) && e.Code == apperr.SlotUnavailable {
		slotConflicts.Add(key, 1)
	}
	return err
}

// Change is an administrator's action on one appointment, as of the version
// their screen showed (Booking & Admin UX §30: re-check before acting).
type Change struct {
	ID      pgtype.UUID
	Version int32
	// Actor is the signed-in administrator.
	Actor pgtype.UUID
	// ToVisitor is Daw Mi's optional message on a decline or cancellation.
	ToVisitor string
	// Notify is false when Daw Mi tells the visitor herself.
	Notify bool
}

// Changed is what the caller does with the queue once the change has
// committed: Tasks to enqueue, Replace to enqueue in place of a waiting task
// with the same id, and Remove to take off the queue.
type Changed struct {
	Tasks, Replace, Remove []platform.Task
	// EndedRoom is the video room the change ended, whose sockets the
	// caller closes once it has committed.
	EndedRoom pgtype.UUID
}

var (
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
	errStaleAppointment    = apperr.New(apperr.StaleVersion, "The appointment changed since it was read; reload it.")
)

func invalidTransition(from Status, action string) error {
	return apperr.New(apperr.InvalidTransition, "A "+string(from)+" appointment cannot be "+action+" now.")
}

// change runs fn on the appointment c names, locked and at c's version, in
// one transaction under the schedule lock, so an admin action, a booking and
// an availability change never interleave.
func change(ctx context.Context, pool *pgxpool.Pool, c Change,
	fn func(q *db.Queries, appt db.Appointment, cur settings.Settings) (Changed, error)) (Changed, error) {
	var out Changed
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		appt, err := q.LockAppointment(ctx, c.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errAppointmentNotFound
		}
		if err != nil {
			return err
		}
		if appt.Version != c.Version {
			return errStaleAppointment
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		out, err = fn(q, appt, cur)
		return err
	})
	return out, err
}

// setStatus moves appt to `to` through the transition table and records
// kind in its history.
func setStatus(ctx context.Context, q db.Querier, appt db.Appointment, to Status, kind string, c Change,
	detail EventDetail, now time.Time) (db.Appointment, error) {
	from := Status(appt.Status)
	if !CanTransition(from, to) {
		return db.Appointment{}, invalidTransition(from, kind)
	}
	updated, err := q.SetAppointmentStatus(ctx, db.SetAppointmentStatusParams{ID: appt.ID, Status: string(to), Now: now})
	if err != nil {
		return db.Appointment{}, err
	}
	return updated, AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: kind, From: from, To: to,
		Actor: "admin", ActorUserID: c.Actor, Detail: detail})
}

// maxNoteLength is appointments_visitor_lengths' bound on admin_note.
const maxNoteLength = 2000

// SetNote replaces the private scheduling note; an empty note clears it.
// The history records the note's length, never its text.
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
