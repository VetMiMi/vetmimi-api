// Package comms stores and sends the practice's emails. Queue writes a
// communication row in the caller's transaction and returns a task; the
// worker's Deliver renders the row's template and sends it through Resend or SMTP.
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
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

type Kind string

type Audience string

type Status string

// Message is about an appointment or, with ContactEnquiryID set, an enquiry.
type Message struct {
	AppointmentID    pgtype.UUID
	ContactEnquiryID pgtype.UUID
	Kind             Kind
	Recipient        string
	Locale           string
	ScheduledFor     time.Time // zero means now
	Text             string    // someone's own words, sent as written
}

const (
	RequestReceived                 Kind = "request_received"
	BookingConfirmed                Kind = "booking_confirmed"
	RequestDeclined                 Kind = "request_declined"
	Rescheduled                     Kind = "rescheduled"
	Cancelled                       Kind = "cancelled"
	Reminder                        Kind = "reminder"
	RequestExpired                  Kind = "request_expired"
	PractitionerNewRequest          Kind = "practitioner_new_request"
	PractitionerNewBooking          Kind = "practitioner_new_booking"
	PractitionerClientCancelled     Kind = "practitioner_client_cancelled"
	PractitionerRescheduleRequested Kind = "practitioner_reschedule_requested"
	PractitionerNewEnquiry          Kind = "practitioner_new_enquiry"
)

var Kinds = []Kind{
	RequestReceived, BookingConfirmed, RequestDeclined, Rescheduled, Cancelled, Reminder,
	RequestExpired, PractitionerNewRequest, PractitionerNewBooking, PractitionerClientCancelled,
	PractitionerRescheduleRequested, PractitionerNewEnquiry,
}

var Locales = []string{"en", "my"}

const (
	Visitor      Audience = "visitor"
	Practitioner Audience = "practitioner"
)

const (
	StatusQueued    Status = "queued"
	StatusSent      Status = "sent"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

var Statuses = []Status{StatusQueued, StatusSent, StatusFailed, StatusCancelled}

func (kind Kind) Audience() Audience {
	if strings.HasPrefix(string(kind), "practitioner_") {
		return Practitioner
	}
	return Visitor
}

func CanTransition(from, to Status) bool {
	if from != StatusQueued {
		return false
	}
	switch to {
	case StatusSent, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// Queue returns the task to enqueue after the caller's transaction commits.
func Queue(ctx context.Context, q db.Querier, message Message) (queue.Task, error) {
	locale := message.Locale
	if message.Kind.Audience() == Practitioner { // Daw Mi's emails are always in English
		locale = "en"
	}
	row, err := q.InsertCommunication(ctx, db.InsertCommunicationParams{
		AppointmentID:    message.AppointmentID,
		ContactEnquiryID: message.ContactEnquiryID,
		Kind:             string(message.Kind),
		Audience:         string(message.Kind.Audience()),
		Recipient:        pgtype.Text{String: strings.ToLower(message.Recipient), Valid: true},
		Locale:           locale,
		ScheduledFor:     sql.NullTime{Time: message.ScheduledFor, Valid: !message.ScheduledFor.IsZero()},
		Message:          pgtype.Text{String: message.Text, Valid: message.Text != ""},
	})
	if err != nil {
		return queue.Task{}, fmt.Errorf("comms: queue %s: %w", message.Kind, err)
	}
	return deliverTask(row.ID, Kind(row.Kind), row.ScheduledFor), nil
}

// Cancel reports false when the row is missing or no longer queued.
func Cancel(ctx context.Context, q db.Querier, id pgtype.UUID, reason string) (bool, error) {
	row, err := q.GetCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return setStatus(ctx, q, row, StatusCancelled, reason)
}

// RescheduleReminderRows moves queued reminders to the current reminder_hours.
func RescheduleReminderRows(ctx context.Context, q db.Querier) ([]queue.Task, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.RescheduleQueuedReminders(ctx, int32(current.ReminderHours))
	if err != nil {
		return nil, err
	}
	tasks := make([]queue.Task, len(rows))
	for i, row := range rows {
		tasks[i] = deliverTask(row.ID, Reminder, row.ScheduledFor)
	}
	return tasks, nil
}

// setStatus reports false when the row is no longer queued.
func setStatus(ctx context.Context, q db.Querier, row db.Communication, to Status, code string) (bool, error) {
	if !CanTransition(Status(row.Status), to) {
		return false, nil
	}
	updated, err := q.SetCommunicationStatus(ctx, db.SetCommunicationStatusParams{
		ID:                row.ID,
		Status:            string(to),
		Error:             pgtype.Text{String: code, Valid: code != ""},
		Attempts:          row.Attempts,
		SentAt:            row.SentAt,
		ProviderMessageID: row.ProviderMessageID,
	})
	if err != nil {
		return false, err
	}
	return updated > 0, nil
}

// keepQueued records a failed attempt on a row that will be sent again.
func keepQueued(ctx context.Context, q db.Querier, row db.Communication, code string) error {
	_, err := q.SetCommunicationStatus(ctx, db.SetCommunicationStatusParams{
		ID:       row.ID,
		Status:   string(StatusQueued),
		Error:    pgtype.Text{String: code, Valid: true},
		Attempts: row.Attempts,
	})
	return err
}
