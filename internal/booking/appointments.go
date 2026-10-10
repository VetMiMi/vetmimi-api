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

const referenceAttempts = 5

// InsertAppointment returns slot_unavailable when appointments_no_overlap refuses the time.
func InsertAppointment(ctx context.Context, q db.Querier, secret []byte, a NewAppointment) (db.Appointment, error) {
	seed, err := NewSeed()
	if err != nil {
		return db.Appointment{}, err
	}
	p := db.InsertAppointmentParams{
		PractitionerID:      a.PractitionerID,
		ServiceID:           a.Service.ID,
		Status:              string(a.Status),
		StartsAt:            a.StartsAt,
		EndsAt:              a.StartsAt.Add(a.Duration),
		DurationMinutes:     int32(a.Duration / time.Minute),
		BusyRange:           BusyRange(a.StartsAt, a.Duration, a.Service).tstzrange(),
		Timezone:            a.Timezone,
		Format:              a.Format,
		Locale:              a.Locale,
		Source:              a.Source,
		VisitorName:         a.VisitorName,
		VisitorEmail:        a.VisitorEmail,
		VisitorPhone:        a.VisitorPhone,
		VisitorNote:         a.VisitorNote,
		PrivacyAckAt:        a.PrivacyAckAt,
		PolicyAckAt:         a.PolicyAckAt,
		HoldExpiresAt:       a.HoldExpiresAt,
		ManagementTokenSeed: seed,
		ManagementTokenHash: tokens.Hash(tokens.Management(secret, seed)),
		CreatedBy:           a.CreatedBy,
		AdminNote:           a.AdminNote,
	}
	for range referenceAttempts {
		if p.Reference, err = tokens.NewReference("VM-"); err != nil {
			return db.Appointment{}, err
		}
		row, err := q.InsertAppointment(ctx, p)
		// No row back means the reference was taken; try another.
		if !errors.Is(err, pgx.ErrNoRows) {
			return row, refusal(err)
		}
	}
	return db.Appointment{}, fmt.Errorf("booking: no free reference after %d attempts", referenceAttempts)
}

// BusyRange is the session plus the service's buffers, fixed when the appointment is written.
func BusyRange(startsAt time.Time, duration time.Duration, s db.Service) Period {
	return Period{
		Start: startsAt.Add(-time.Duration(s.BufferBeforeMinutes) * time.Minute),
		End:   startsAt.Add(duration + time.Duration(s.BufferAfterMinutes)*time.Minute),
	}
}

// NewSeed returns a management-link seed. It is stored so the worker can render the link; the token is not.
func NewSeed() ([]byte, error) {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}

// afterConfirm is what every newly confirmed appointment gets: its video room, confirmation and reminder.
func afterConfirm(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, cur settings.Settings,
	now time.Time, notify bool) ([]queue.Task, error) {
	// The room comes first so the emails can carry its join link.
	room, err := video.CreateRoom(ctx, q, secret, appt, cur.MeetingLinkMode, now)
	if err != nil {
		return nil, err
	}
	var tasks []queue.Task
	if notify {
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
			Kind: comms.BookingConfirmed, Recipient: appt.VisitorEmail, Locale: appt.Locale})
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	reminder, err := ScheduleReminder(ctx, q, appt, cur.ReminderHours, now)
	return append(append(tasks, reminder...), room...), err
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

func AppendEvent(ctx context.Context, q db.Querier, e Event) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	return q.InsertAppointmentEvent(ctx, db.InsertAppointmentEventParams{
		AppointmentID: e.AppointmentID,
		Kind:          e.Kind,
		FromStatus:    pgtype.Text{String: string(e.From), Valid: e.From != ""},
		ToStatus:      pgtype.Text{String: string(e.To), Valid: e.To != ""},
		PreviousRange: optionalRange(e.Previous),
		NewRange:      optionalRange(e.New),
		Actor:         e.Actor,
		ActorUserID:   e.ActorUserID,
		Detail:        detail,
	})
}

func optionalRange(p *Period) pgtype.Range[pgtype.Timestamptz] {
	if p == nil {
		return pgtype.Range[pgtype.Timestamptz]{}
	}
	return p.tstzrange()
}

func optionalText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	return pgtype.Text{String: s, Valid: s != ""}
}
