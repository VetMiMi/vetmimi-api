package comms

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/resend/resend-go/v2"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// maxAttempts bounds the sends of one row (ADR-006: retries with backoff up
// to 5 times, then failed). The row counts them, so the limit holds whatever
// asynq's own retry count is.
const maxAttempts = 5

var emailsFailed = expvar.NewInt("emails_failed")

// Tasks runs the comms task handlers in the worker.
type Tasks struct {
	Pool  *pgxpool.Pool
	Queue *platform.Queue
	// Resend is nil in development, where a send is logged, not made.
	Resend  *resend.Client
	From    string
	SiteURL string
	Log     *slog.Logger
	Now     clock.Now
}

// Register adds the comms handlers and the sweep schedule to w.
func (t *Tasks) Register(w *platform.Worker) {
	w.Handle(TaskDeliver, t.Deliver)
	w.Handle(TaskSweep, t.Sweep)
	w.Handle(TaskRescheduleReminders, t.RescheduleReminders)
	w.Every("@every 5m", TaskSweep)
}

// Deliver sends one queued communication. It holds the row's lock while it
// sends, so a second worker given the same task skips it, and passes the row
// id to Resend as the idempotency key, so a retry after a lost reply never
// sends twice. It returns an error only when the send should be retried.
func (t *Tasks) Deliver(ctx context.Context, payload []byte) error {
	var p deliverPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("comms: deliver payload: %w", err)
	}
	var id pgtype.UUID
	if err := id.Scan(p.CommunicationID); err != nil {
		return fmt.Errorf("comms: deliver payload: %w", err)
	}

	tx, err := t.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := db.New(tx)

	row, err := q.LockCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted, or another worker holds it
	}
	if err != nil {
		return err
	}
	now := t.Now()
	// A reminder moved later still has its old task; the task for the new
	// time, or the sweep, sends it.
	if Status(row.Status) != StatusQueued || row.ScheduledFor.After(now) {
		return nil
	}

	email, skip, err := t.compose(ctx, q, row, now)
	if err != nil {
		return err
	}
	if skip != "" {
		if _, err := setStatus(ctx, q, row, StatusCancelled, skip); err != nil {
			return err
		}
		t.Log.InfoContext(ctx, "communication skipped", "kind", row.Kind,
			"communication_id", row.ID.String(), "skip", skip)
		return tx.Commit(ctx)
	}

	providerID, sendErr := t.send(ctx, row, email)
	row.Attempts++
	if sendErr != nil {
		return t.recordFailure(ctx, tx, row, sendErr)
	}
	row.SentAt = sql.NullTime{Time: now, Valid: true}
	row.ProviderMessageID = pgtype.Text{String: providerID, Valid: true}
	if _, err := setStatus(ctx, q, row, StatusSent, ""); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	t.Log.InfoContext(ctx, "email sent", "kind", row.Kind, "communication_id", row.ID.String())
	return nil
}

// recordFailure keeps the row queued for asynq to retry, or, on the last
// attempt, marks it failed for the admin's "Attention required".
func (t *Tasks) recordFailure(ctx context.Context, tx pgx.Tx, row db.Communication, sendErr error) error {
	code := errorCode(sendErr)
	q := db.New(tx)
	if row.Attempts < maxAttempts {
		if _, err := q.SetCommunicationStatus(ctx, db.SetCommunicationStatusParams{
			ID: row.ID, Status: string(StatusQueued), Error: pgtype.Text{String: code, Valid: true},
			Attempts: row.Attempts,
		}); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return fmt.Errorf("comms: send failed: %s", code)
	}
	if _, err := setStatus(ctx, q, row, StatusFailed, code); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	emailsFailed.Add(1)
	t.Log.ErrorContext(ctx, "email failed", "kind", row.Kind, "communication_id", row.ID.String(),
		"attempts", row.Attempts, "error_code", code)
	return nil
}

// compose renders row, or names why it must not be sent.
func (t *Tasks) compose(ctx context.Context, q *db.Queries, row db.Communication, now time.Time) (Email, string, error) {
	if !row.AppointmentID.Valid {
		// Enquiry messages need the contact_enquiries table (issue #51).
		return Email{}, "", errors.New("comms: enquiry communications are not supported yet")
	}
	appt, err := q.GetAppointmentForMessage(ctx, row.AppointmentID)
	if err != nil {
		return Email{}, "", err
	}
	s, err := settings.Load(ctx, q)
	if err != nil {
		return Email{}, "", err
	}
	if Kind(row.Kind) == Reminder {
		if skip := reminderSkip(appt, s.ReminderHours, row.ScheduledFor, now); skip != "" {
			return Email{}, skip, nil
		}
	}
	var previous time.Time
	if Kind(row.Kind) == Rescheduled {
		r, err := q.PreviousRangeOf(ctx, row.AppointmentID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return Email{}, "", err
		}
		previous = r.Lower.Time
	}
	data, err := DataFor(appt, s, t.SiteURL, row.Locale, previous)
	if err != nil {
		return Email{}, "", err
	}
	data.MessageToVisitor = row.Message.String
	email, err := Render(Kind(row.Kind), row.Locale, data)
	email.ReplyTo = s.ContactEmail
	return email, "", err
}
