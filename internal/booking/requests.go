package booking

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

const httpCreated = 201

var (
	errOutsideWindow = apperr.New(apperr.OutsideBookingWindow,
		"The start is inside the minimum notice or beyond the furthest bookable day.")
	errAcknowledgement = apperr.New(apperr.AcknowledgementRequired,
		"The privacy notice and the booking policy must both be acknowledged.")
)

type Request struct {
	IdempotencyKey string
	// Body is the raw request; the idempotency key's hash covers it.
	Body                                    []byte
	Service                                 string
	StartsAt                                time.Time
	Format, Locale                          string
	VisitorName, VisitorEmail               string
	VisitorPhone, VisitorNote               string
	PrivacyAcknowledged, PolicyAcknowledged bool
}

// Receipt's JSON is the stored idempotent response, so it matches AppointmentRequestReceipt.
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

type ReceiptService struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// Requested's Tasks are enqueued after commit; a replay has none.
type Requested struct {
	AppointmentID pgtype.UUID
	Receipt       Receipt
	Replayed      bool
	Tasks         []queue.Task
}

// RequestAppointment stores a pending request, or a confirmed booking in instant mode for a "book" service.
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
		out, err = createRequest(ctx, q, secret, r, now)
		if err != nil {
			return err
		}
		return idempotency.Finish(ctx, q, idempotency.PublicAppointment, r.IdempotencyKey,
			out.AppointmentID, httpCreated, out.Receipt)
	})
	return out, countConflict("website", err)
}

func createRequest(ctx context.Context, q *db.Queries, secret []byte, r Request, now time.Time) (Requested, error) {
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
	a := requestedAppointment(practitionerID, svc, cur, r, now)
	appt, err := InsertAppointment(ctx, q, secret, a)
	if err != nil {
		return Requested{}, withAlternatives(err)
	}
	appointmentsCreated.Add(a.Source, 1)
	if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "created", To: a.Status, Actor: "visitor"}); err != nil {
		return Requested{}, err
	}
	tasks, err := notifyRequested(ctx, q, secret, appt, cur, now)
	if err != nil {
		return Requested{}, err
	}
	return Requested{AppointmentID: appt.ID, Tasks: tasks, Receipt: receiptOf(appt, svc, r.Locale)}, nil
}

func requestedAppointment(practitionerID pgtype.UUID, svc db.Service, cur settings.Settings, r Request,
	now time.Time) NewAppointment {
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
	return a
}

func receiptOf(appt db.Appointment, svc db.Service, locale string) Receipt {
	return Receipt{
		Reference:       appt.Reference,
		Status:          Status(appt.Status),
		Service:         ReceiptService{Slug: svc.Slug, Name: inLocale(svc.Name, locale)},
		StartsAt:        appt.StartsAt.UTC(),
		EndsAt:          appt.EndsAt.UTC(),
		DurationMinutes: int(appt.DurationMinutes),
		Timezone:        appt.Timezone,
		Format:          appt.Format,
	}
}

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

// checkRequest applies the rules in the order the visitor can act on them.
func checkRequest(ctx context.Context, q db.Querier, cur settings.Settings, r Request, now time.Time) (db.Service, error) {
	if !cur.PublicBookingEnabled {
		return db.Service{}, errBookingPaused
	}
	svc, err := q.GetServiceBySlug(ctx, r.Service)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errNotBookable
	}
	if err != nil {
		return db.Service{}, err
	}
	if err := checkService(svc, r.Format); err != nil {
		return db.Service{}, err
	}
	if !r.PrivacyAcknowledged || !r.PolicyAcknowledged {
		return db.Service{}, errAcknowledgement
	}
	if err := checkWindow(cur, r.StartsAt, now); err != nil {
		return db.Service{}, err
	}
	return svc, checkSlot(ctx, q, cur, slotRequest{Service: svc, Start: r.StartsAt}, now)
}

func checkService(svc db.Service, format string) error {
	if !bookable(svc) {
		return errNotBookable
	}
	if !slices.Contains(svc.Formats, format) {
		e := unprocessable("/format", "is not offered for this service")
		return &e
	}
	return nil
}

func checkWindow(cur settings.Settings, start, now time.Time) error {
	loc, err := cur.Location()
	if err != nil {
		return err
	}
	today := now.In(loc)
	horizon := time.Date(today.Year(), today.Month(), today.Day()+cur.MaxAdvanceDays+1, 0, 0, 0, 0, loc)
	if start.Before(now.Add(time.Duration(cur.MinNoticeHours)*time.Hour)) || !start.Before(horizon) {
		return errOutsideWindow
	}
	return nil
}
