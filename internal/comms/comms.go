// Package comms keeps the communications history and delivers it (ADR-006).
// A message is first a row, written in the transaction that caused it; the
// caller enqueues the task Queue returns after commit, and the worker renders
// and sends it through Resend.
package comms

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
)

// Task types this package handles (docs/architecture.md, "Background jobs").
const (
	TaskDeliver             = "comms:deliver"
	TaskSweep               = "comms:sweep"
	TaskRescheduleReminders = "comms:reschedule-reminders"
)

// Message is one email to queue about an appointment.
type Message struct {
	AppointmentID pgtype.UUID
	Kind          Kind
	Recipient     string
	// Locale is the visitor's; practitioner kinds are always rendered in en,
	// because Daw Mi's locale is not a setting yet.
	Locale string
	// ScheduledFor is when to send; zero means now.
	ScheduledFor time.Time
	// ToVisitor is Daw Mi's own message on a decline or cancellation, sent
	// as she wrote it; empty for none.
	ToVisitor string
}

// Queue inserts m as a queued row through q, which is the caller's
// transaction, and returns the task to enqueue once it commits.
func Queue(ctx context.Context, q db.Querier, m Message) (platform.Task, error) {
	locale := m.Locale
	if m.Kind.Audience() == Practitioner {
		locale = "en"
	}
	row, err := q.InsertCommunication(ctx, db.InsertCommunicationParams{
		AppointmentID: m.AppointmentID,
		Kind:          string(m.Kind),
		Audience:      string(m.Kind.Audience()),
		Recipient:     pgtype.Text{String: strings.ToLower(m.Recipient), Valid: true},
		Locale:        locale,
		ScheduledFor:  sql.NullTime{Time: m.ScheduledFor, Valid: !m.ScheduledFor.IsZero()},
		Message:       pgtype.Text{String: m.ToVisitor, Valid: m.ToVisitor != ""},
	})
	if err != nil {
		return platform.Task{}, fmt.Errorf("comms: queue %s: %w", m.Kind, err)
	}
	return deliverTask(row.ID, Kind(row.Kind), row.ScheduledFor), nil
}

// Cancel moves a queued communication to cancelled, recording reason, a
// short code, in error. A row already sent, failed or cancelled is left as
// it is.
func Cancel(ctx context.Context, q db.Querier, id pgtype.UUID, reason string) error {
	row, err := q.GetCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = setStatus(ctx, q, row, StatusCancelled, reason)
	return err
}

// setStatus moves row to status through the transition table, recording
// code in error. It reports false when the table refuses the change.
func setStatus(ctx context.Context, q db.Querier, row db.Communication, to Status, code string) (bool, error) {
	if !CanTransition(Status(row.Status), to) {
		return false, nil
	}
	_, err := q.SetCommunicationStatus(ctx, db.SetCommunicationStatusParams{
		ID:                row.ID,
		Status:            string(to),
		Error:             pgtype.Text{String: code, Valid: code != ""},
		Attempts:          row.Attempts,
		SentAt:            row.SentAt,
		ProviderMessageID: row.ProviderMessageID,
	})
	return err == nil, err
}

// deliverTask carries only the row id; the worker loads the rest. Visitor
// mail goes first when the worker is busy.
func deliverTask(id pgtype.UUID, kind Kind, at time.Time) platform.Task {
	queue := platform.QueueCritical
	if kind.Audience() == Practitioner {
		queue = platform.QueueDefault
	}
	return platform.Task{
		Type:      TaskDeliver,
		Payload:   deliverPayload{CommunicationID: id.String()},
		ID:        "comms:" + id.String(),
		ProcessAt: at,
		Queue:     queue,
	}
}

type deliverPayload struct {
	CommunicationID string `json:"communication_id"`
}
