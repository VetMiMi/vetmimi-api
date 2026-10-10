// Package comms stores and sends the practice's emails. Queue writes a
// communication row in the caller's transaction and returns a task; the
// worker's Deliver renders the row's template and sends it through Resend.
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

const (
	TaskDeliver             = "comms:deliver"
	TaskSweep               = "comms:sweep"
	TaskRescheduleReminders = "comms:reschedule-reminders"
)

type Kind string

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

type Audience string

const (
	Visitor      Audience = "visitor"
	Practitioner Audience = "practitioner"
)

func (k Kind) Audience() Audience {
	if strings.HasPrefix(string(k), "practitioner_") {
		return Practitioner
	}
	return Visitor
}

type Status string

const (
	StatusQueued    Status = "queued"
	StatusSent      Status = "sent"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

var Statuses = []Status{StatusQueued, StatusSent, StatusFailed, StatusCancelled}

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

// Queue returns the task to enqueue after the caller's transaction commits.
func Queue(ctx context.Context, q db.Querier, m Message) (platform.Task, error) {
	locale := m.Locale
	if m.Kind.Audience() == Practitioner { // Daw Mi's emails are always in English
		locale = "en"
	}
	row, err := q.InsertCommunication(ctx, db.InsertCommunicationParams{
		AppointmentID:    m.AppointmentID,
		ContactEnquiryID: m.ContactEnquiryID,
		Kind:             string(m.Kind),
		Audience:         string(m.Kind.Audience()),
		Recipient:        pgtype.Text{String: strings.ToLower(m.Recipient), Valid: true},
		Locale:           locale,
		ScheduledFor:     sql.NullTime{Time: m.ScheduledFor, Valid: !m.ScheduledFor.IsZero()},
		Message:          pgtype.Text{String: m.Text, Valid: m.Text != ""},
	})
	if err != nil {
		return platform.Task{}, fmt.Errorf("comms: queue %s: %w", m.Kind, err)
	}
	return deliverTask(row.ID, Kind(row.Kind), row.ScheduledFor), nil
}

func Cancel(ctx context.Context, q db.Querier, id pgtype.UUID, reason string) error {
	row, err := q.GetCommunication(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return setStatus(ctx, q, row, StatusCancelled, reason)
}

// setStatus does nothing when CanTransition refuses the change.
func setStatus(ctx context.Context, q db.Querier, row db.Communication, to Status, code string) error {
	if !CanTransition(Status(row.Status), to) {
		return nil
	}
	_, err := q.SetCommunicationStatus(ctx, db.SetCommunicationStatusParams{
		ID:                row.ID,
		Status:            string(to),
		Error:             pgtype.Text{String: code, Valid: code != ""},
		Attempts:          row.Attempts,
		SentAt:            row.SentAt,
		ProviderMessageID: row.ProviderMessageID,
	})
	return err
}

type deliverPayload struct {
	CommunicationID string `json:"communication_id"`
}

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
