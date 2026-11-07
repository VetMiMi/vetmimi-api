package booking

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

type NewAppointment struct {
	PractitionerID pgtype.UUID
	Service        db.Service
	StartsAt       time.Time
	Duration       time.Duration
	Status         Status
	Timezone       string
	Format         string
	Locale         string
	Source         string
	VisitorName    string
	VisitorEmail   string
	VisitorPhone   pgtype.Text
	VisitorNote    pgtype.Text
	PrivacyAckAt   sql.NullTime
	PolicyAckAt    sql.NullTime
	HoldExpiresAt  sql.NullTime
	CreatedBy      pgtype.UUID
	AdminNote      pgtype.Text
}

// Event is one entry in an appointment's history.
type Event struct {
	AppointmentID pgtype.UUID
	Kind          string
	From, To      Status
	Previous, New *Period
	Actor         string
	ActorUserID   pgtype.UUID
	Detail        EventDetail
}

// EventDetail is a struct, not a map, so a name, email or note has nowhere to go.
type EventDetail struct {
	LateCancellation *bool       `json:"late_cancellation,omitempty"`
	By               string      `json:"by,omitempty"`
	Source           string      `json:"source,omitempty"`
	NoteLength       *int        `json:"length,omitempty"`
	Preferred        []time.Time `json:"preferred,omitempty"`
}

const referenceAttempts = 5

// InsertAppointment returns slot_unavailable when appointments_no_overlap refuses the time.
func InsertAppointment(ctx context.Context, q db.Querier, secret []byte, appointment NewAppointment) (db.Appointment, error) {
	seed, err := NewSeed()
	if err != nil {
		return db.Appointment{}, err
	}
	params := db.InsertAppointmentParams{
		PractitionerID:      appointment.PractitionerID,
		ServiceID:           appointment.Service.ID,
		Status:              string(appointment.Status),
		StartsAt:            appointment.StartsAt,
		EndsAt:              appointment.StartsAt.Add(appointment.Duration),
		DurationMinutes:     int32(appointment.Duration / time.Minute),
		BusyRange:           BusyRange(appointment.StartsAt, appointment.Duration, appointment.Service).tstzrange(),
		Timezone:            appointment.Timezone,
		Format:              appointment.Format,
		Locale:              appointment.Locale,
		Source:              appointment.Source,
		VisitorName:         appointment.VisitorName,
		VisitorEmail:        appointment.VisitorEmail,
		VisitorPhone:        appointment.VisitorPhone,
		VisitorNote:         appointment.VisitorNote,
		PrivacyAckAt:        appointment.PrivacyAckAt,
		PolicyAckAt:         appointment.PolicyAckAt,
		HoldExpiresAt:       appointment.HoldExpiresAt,
		ManagementTokenSeed: seed,
		ManagementTokenHash: tokens.Hash(tokens.Management(secret, seed)),
		CreatedBy:           appointment.CreatedBy,
		AdminNote:           appointment.AdminNote,
	}
	for range referenceAttempts {
		params.Reference, err = tokens.NewReference("VM-")
		if err != nil {
			return db.Appointment{}, err
		}
		row, err := q.InsertAppointment(ctx, params)
		// No row back means the reference was taken; try another.
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return db.Appointment{}, refusal(err)
		}
		return row, nil
	}
	return db.Appointment{}, fmt.Errorf("booking: no free reference after %d attempts", referenceAttempts)
}

// BusyRange is the session plus the service's buffers, fixed when the appointment is written.
func BusyRange(startsAt time.Time, duration time.Duration, service db.Service) Period {
	return Period{
		Start: startsAt.Add(-time.Duration(service.BufferBeforeMinutes) * time.Minute),
		End:   startsAt.Add(duration + time.Duration(service.BufferAfterMinutes)*time.Minute),
	}
}

// NewSeed returns a management-link seed. It is stored so the worker can render the link; the token is not.
func NewSeed() ([]byte, error) {
	seed := make([]byte, 32)
	_, err := rand.Read(seed)
	if err != nil {
		return nil, err
	}
	return seed, nil
}

func AppendEvent(ctx context.Context, q db.Querier, event Event) error {
	detail, err := json.Marshal(event.Detail)
	if err != nil {
		return err
	}
	params := db.InsertAppointmentEventParams{
		AppointmentID: event.AppointmentID,
		Kind:          event.Kind,
		FromStatus:    pgtype.Text{String: string(event.From), Valid: event.From != ""},
		ToStatus:      pgtype.Text{String: string(event.To), Valid: event.To != ""},
		PreviousRange: optionalRange(event.Previous),
		NewRange:      optionalRange(event.New),
		Actor:         event.Actor,
		ActorUserID:   event.ActorUserID,
		Detail:        detail,
	}
	return q.InsertAppointmentEvent(ctx, params)
}

// afterConfirm is what every newly confirmed appointment gets: its video room, confirmation and reminder.
func afterConfirm(ctx context.Context, q db.Querier, secret []byte, appointment db.Appointment,
	current settings.Settings, now time.Time, notify bool) ([]queue.Task, error) {
	// The room comes first so the emails can carry its join link.
	room, err := video.CreateRoom(ctx, q, secret, appointment, current.MeetingLinkMode, now)
	if err != nil {
		return nil, err
	}
	var tasks []queue.Task
	if notify {
		message := comms.Message{
			AppointmentID: appointment.ID,
			Kind:          comms.BookingConfirmed,
			Recipient:     appointment.VisitorEmail,
			Locale:        appointment.Locale,
		}
		task, err := comms.Queue(ctx, q, message)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	reminder, err := ScheduleReminder(ctx, q, appointment, current.ReminderHours, now)
	if err != nil {
		return nil, err
	}
	tasks = append(tasks, reminder...)
	tasks = append(tasks, room...)
	return tasks, nil
}

func optionalRange(period *Period) pgtype.Range[pgtype.Timestamptz] {
	if period == nil {
		return pgtype.Range[pgtype.Timestamptz]{}
	}
	return period.tstzrange()
}

func optionalText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	return pgtype.Text{String: s, Valid: s != ""}
}
