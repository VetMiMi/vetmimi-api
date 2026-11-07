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
	Reference       string     `json:"reference"`
	Status          Status     `json:"status"`
	Service         ServiceRef `json:"service"`
	StartsAt        time.Time  `json:"startsAt"`
	EndsAt          time.Time  `json:"endsAt"`
	DurationMinutes int        `json:"durationMinutes"`
	Timezone        string     `json:"timezone"`
	Format          string     `json:"format"`
}

type ServiceRef struct {
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

const httpCreated = 201

var (
	errOutsideWindow = apperr.New(apperr.OutsideBookingWindow,
		"The start is inside the minimum notice or beyond the furthest bookable day.")
	errAcknowledgement = apperr.New(apperr.AcknowledgementRequired,
		"The privacy notice and the booking policy must both be acknowledged.")
)

// RequestAppointment stores a pending request, or a confirmed booking in instant mode for a "book" service.
func RequestAppointment(ctx context.Context, pool *pgxpool.Pool, secret []byte, request Request,
	now time.Time) (Requested, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Requested{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Requested{}, err
	}
	stored, err := idempotency.Begin(ctx, q, idempotency.PublicAppointment, request.IdempotencyKey, request.Body, now)
	if err != nil {
		return Requested{}, err
	}
	// A replay wrote nothing, so it has nothing to commit.
	if stored != nil {
		replayed := Requested{
			AppointmentID: stored.ResourceID,
			Replayed:      true,
		}
		err = json.Unmarshal(stored.Body, &replayed.Receipt)
		if err != nil {
			return Requested{}, err
		}
		return replayed, nil
	}
	requested, err := createRequest(ctx, q, secret, request, now)
	if err != nil {
		return Requested{}, countConflict("website", err)
	}
	err = idempotency.Finish(ctx, q, idempotency.PublicAppointment, request.IdempotencyKey,
		requested.AppointmentID, httpCreated, requested.Receipt)
	if err != nil {
		return Requested{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Requested{}, err
	}
	return requested, nil
}

func createRequest(ctx context.Context, q db.Querier, secret []byte, request Request, now time.Time) (Requested, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Requested{}, err
	}
	service, err := checkRequest(ctx, q, current, request, now)
	if err != nil {
		return Requested{}, err
	}
	practitionerID, err := q.GetPractitionerID(ctx)
	if err != nil {
		return Requested{}, err
	}
	newAppointment := requestedAppointment(practitionerID, service, current, request, now)
	appointment, err := InsertAppointment(ctx, q, secret, newAppointment)
	if err != nil {
		return Requested{}, withAlternatives(err)
	}
	appointmentsCreated.Add(newAppointment.Source, 1)
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "created",
		To:            newAppointment.Status,
		Actor:         "visitor",
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return Requested{}, err
	}
	tasks, err := notifyRequested(ctx, q, secret, appointment, current, now)
	if err != nil {
		return Requested{}, err
	}
	requested := Requested{
		AppointmentID: appointment.ID,
		Tasks:         tasks,
		Receipt:       receiptOf(appointment, service, request.Locale),
	}
	return requested, nil
}

func requestedAppointment(practitionerID pgtype.UUID, service db.Service, current settings.Settings, request Request,
	now time.Time) NewAppointment {
	status := Pending
	if current.BookingMode == "instant" && service.BookingAction == "book" {
		status = Confirmed
	}
	acknowledged := sql.NullTime{Time: now, Valid: true}
	appointment := NewAppointment{
		PractitionerID: practitionerID,
		Service:        service,
		StartsAt:       request.StartsAt,
		Duration:       time.Duration(service.DurationMinutes.Int32) * time.Minute,
		Status:         status,
		Timezone:       current.Timezone,
		Format:         request.Format,
		Locale:         request.Locale,
		Source:         "website",
		VisitorName:    strings.TrimSpace(request.VisitorName),
		VisitorEmail:   strings.ToLower(strings.TrimSpace(request.VisitorEmail)),
		VisitorPhone:   optionalText(request.VisitorPhone),
		VisitorNote:    optionalText(request.VisitorNote),
		PrivacyAckAt:   acknowledged,
		PolicyAckAt:    acknowledged,
	}
	if status == Pending {
		appointment.HoldExpiresAt = holdUntil(current, request.StartsAt, now)
	}
	return appointment
}

func receiptOf(appointment db.Appointment, service db.Service, locale string) Receipt {
	return Receipt{
		Reference:       appointment.Reference,
		Status:          Status(appointment.Status),
		Service:         ServiceRef{Slug: service.Slug, Name: inLocale(service.Name, locale)},
		StartsAt:        appointment.StartsAt.UTC(),
		EndsAt:          appointment.EndsAt.UTC(),
		DurationMinutes: int(appointment.DurationMinutes),
		Timezone:        appointment.Timezone,
		Format:          appointment.Format,
	}
}

func notifyRequested(ctx context.Context, q db.Querier, secret []byte, appointment db.Appointment,
	current settings.Settings, now time.Time) ([]queue.Task, error) {
	if Status(appointment.Status) == Confirmed {
		tasks, err := afterConfirm(ctx, q, secret, appointment, current, now, true)
		if err != nil {
			return nil, err
		}
		newBooking := comms.Message{
			AppointmentID: appointment.ID,
			Kind:          comms.PractitionerNewBooking,
			Recipient:     current.ContactEmail,
		}
		task, err := comms.Queue(ctx, q, newBooking)
		if err != nil {
			return nil, err
		}
		return append(tasks, task), nil
	}
	received := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.RequestReceived,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
	}
	visitor, err := comms.Queue(ctx, q, received)
	if err != nil {
		return nil, err
	}
	newRequest := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.PractitionerNewRequest,
		Recipient:     current.ContactEmail,
	}
	practitioner, err := comms.Queue(ctx, q, newRequest)
	if err != nil {
		return nil, err
	}
	return []queue.Task{visitor, practitioner, holdTask(appointment.ID, appointment.HoldExpiresAt.Time)}, nil
}

// checkRequest applies the rules in the order the visitor can act on them.
func checkRequest(ctx context.Context, q db.Querier, current settings.Settings, request Request,
	now time.Time) (db.Service, error) {
	if !current.PublicBookingEnabled {
		return db.Service{}, errBookingPaused
	}
	service, err := q.GetServiceBySlug(ctx, request.Service)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Service{}, errNotBookable
	}
	if err != nil {
		return db.Service{}, err
	}
	err = checkService(service, request.Format)
	if err != nil {
		return db.Service{}, err
	}
	if !request.PrivacyAcknowledged || !request.PolicyAcknowledged {
		return db.Service{}, errAcknowledgement
	}
	err = checkWindow(current, request.StartsAt, now)
	if err != nil {
		return db.Service{}, err
	}
	err = checkSlot(ctx, q, current, slotRequest{Service: service, Start: request.StartsAt}, now)
	if err != nil {
		return db.Service{}, err
	}
	return service, nil
}

func checkService(service db.Service, format string) error {
	if !bookable(service) {
		return errNotBookable
	}
	if !slices.Contains(service.Formats, format) {
		refused := unprocessable("/format", "is not offered for this service")
		return &refused
	}
	return nil
}

func checkWindow(current settings.Settings, start, now time.Time) error {
	loc, err := current.Location()
	if err != nil {
		return err
	}
	today := now.In(loc)
	horizon := time.Date(today.Year(), today.Month(), today.Day()+current.MaxAdvanceDays+1, 0, 0, 0, 0, loc)
	if start.Before(now.Add(time.Duration(current.MinNoticeHours)*time.Hour)) || !start.Before(horizon) {
		return errOutsideWindow
	}
	return nil
}
