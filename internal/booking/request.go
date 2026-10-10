package booking

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// Request is a visitor's appointment request from the public site.
type Request struct {
	IdempotencyKey string
	// Body is the request as the handler received it; the key's hash covers it.
	Body                                    []byte
	Service                                 string
	StartsAt                                time.Time
	Format, Locale                          string
	VisitorName, VisitorEmail               string
	VisitorPhone, VisitorNote               string
	PrivacyAcknowledged, PolicyAcknowledged bool
}

// Receipt is what the visitor is told once the request is stored. Its JSON
// is the stored idempotent response, so it matches AppointmentRequestReceipt.
type Receipt struct {
	Reference       string         `json:"reference"`
	Status          Status         `json:"status"`
	Service         ReceiptService `json:"service"`
	StartsAt        time.Time      `json:"startsAt"`
	EndsAt          time.Time      `json:"endsAt"`
	DurationMinutes int            `json:"durationMinutes"`
	Timezone        string         `json:"timezone"`
	Format          string         `json:"format"`
}

// ReceiptService names the service in the visitor's locale.
type ReceiptService struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Requested is the outcome of RequestAppointment. Tasks are to be enqueued
// after it returns; a replay has none.
type Requested struct {
	AppointmentID pgtype.UUID
	Receipt       Receipt
	Replayed      bool
	Tasks         []queue.Task
}

// created is the status the stored response replays with.
const created = 201

// RequestAppointment stores a visitor's request in one transaction under the
// schedule lock (docs/architecture.md, walkthrough 1): it claims the
// idempotency key, checks the request against the booking rules, requires
// the start to be one of the free slots right now, inserts the appointment
// and queues its emails. Under request & approval it is pending and holds
// its slot until hold_expires_at; in instant mode a service whose action is
// book is confirmed at once. The same key and body again return the first
// receipt and create nothing.
func RequestAppointment(ctx context.Context, pool *pgxpool.Pool, secret []byte, r Request, now time.Time) (Requested, error) {
	var out Requested
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		stored, err := idempotency.Begin(ctx, q, idempotency.PublicAppointment, r.IdempotencyKey, r.Body, now)
		if err != nil {
			return err
		}
		if stored != nil {
			out = Requested{AppointmentID: stored.ResourceID, Replayed: true}
			return json.Unmarshal(stored.Body, &out.Receipt)
		}
		out, err = request(ctx, q, secret, r, now)
		if err != nil {
			return err
		}
		return idempotency.Finish(ctx, q, idempotency.PublicAppointment, r.IdempotencyKey,
			out.AppointmentID, created, out.Receipt)
	})
	return out, countConflict("website", err)
}

func request(ctx context.Context, q *db.Queries, secret []byte, r Request, now time.Time) (Requested, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Requested{}, err
	}
	svc, err := checkRequest(ctx, q, cur, r, now)
	if err != nil {
		return Requested{}, err
	}
	practitionerID, err := q.GetPractitionerID(ctx)
	if err != nil {
		return Requested{}, err
	}

	status := Pending
	if cur.BookingMode == "instant" && svc.BookingAction == "book" {
		status = Confirmed
	}
	ack := sql.NullTime{Time: now, Valid: true}
	a := NewAppointment{
		PractitionerID: practitionerID,
		Service:        svc,
		StartsAt:       r.StartsAt,
		Duration:       time.Duration(svc.DurationMinutes.Int32) * time.Minute,
		Status:         status,
		Timezone:       cur.Timezone,
		Format:         r.Format,
		Locale:         r.Locale,
		Source:         "website",
		VisitorName:    strings.TrimSpace(r.VisitorName),
		VisitorEmail:   strings.ToLower(strings.TrimSpace(r.VisitorEmail)),
		VisitorPhone:   optionalText(r.VisitorPhone),
		VisitorNote:    optionalText(r.VisitorNote),
		PrivacyAckAt:   ack,
		PolicyAckAt:    ack,
	}
	if status == Pending {
		a.HoldExpiresAt = holdUntil(cur, r.StartsAt, now)
	}
	appt, err := InsertAppointment(ctx, q, secret, a)
	if err != nil {
		return Requested{}, withAlternatives(err)
	}
	appointmentsCreated.Add(a.Source, 1)
	if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "created", To: status, Actor: "visitor"}); err != nil {
		return Requested{}, err
	}
	tasks, err := notifyRequested(ctx, q, secret, appt, cur, now)
	if err != nil {
		return Requested{}, err
	}
	return Requested{
		AppointmentID: appt.ID,
		Tasks:         tasks,
		Receipt: Receipt{
			Reference:       appt.Reference,
			Status:          status,
			Service:         ReceiptService{Slug: svc.Slug, Name: inLocale(svc.Name, r.Locale)},
			StartsAt:        appt.StartsAt.UTC(),
			EndsAt:          appt.EndsAt.UTC(),
			DurationMinutes: int(appt.DurationMinutes),
			Timezone:        appt.Timezone,
			Format:          appt.Format,
		},
	}, nil
}

// notifyRequested queues the emails a new appointment sends and returns the
// tasks for them and, while it is pending, for its hold's expiry.
func notifyRequested(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, cur settings.Settings,
	now time.Time) ([]queue.Task, error) {
	if Status(appt.Status) == Confirmed {
		tasks, err := afterConfirm(ctx, q, secret, appt, cur, now, true)
		if err != nil {
			return nil, err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
			Kind: comms.PractitionerNewBooking, Recipient: cur.ContactEmail})
		return append(tasks, task), err
	}
	visitor, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
		Kind: comms.RequestReceived, Recipient: appt.VisitorEmail, Locale: appt.Locale})
	if err != nil {
		return nil, err
	}
	practitioner, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
		Kind: comms.PractitionerNewRequest, Recipient: cur.ContactEmail})
	if err != nil {
		return nil, err
	}
	return []queue.Task{visitor, practitioner, holdTask(appt.ID, appt.HoldExpiresAt.Time)}, nil
}

// afterConfirm queues what confirming an appointment sends the visitor: the
// confirmation, unless Daw Mi tells them herself, and the reminder. An
// online appointment gets its video room first, so the emails can carry the
// join link. Confirmation, instant booking and manual booking all end here.
func afterConfirm(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, cur settings.Settings,
	now time.Time, notify bool) ([]queue.Task, error) {
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

// holdUntil is when a request made now for start stops holding its slot:
// after pending_hold_hours, or at the start if that comes first.
func holdUntil(cur settings.Settings, start, now time.Time) sql.NullTime {
	hold := now.Add(time.Duration(cur.PendingHoldHours) * time.Hour)
	if start.Before(hold) {
		hold = start
	}
	return sql.NullTime{Time: hold, Valid: true}
}

func optionalText(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	return pgtype.Text{String: s, Valid: s != ""}
}
