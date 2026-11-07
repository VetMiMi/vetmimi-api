package booking

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// Manual is an appointment Daw Mi takes by phone or email.
type Manual struct {
	IdempotencyKey string
	// Body is the raw request; the idempotency key's hash covers it.
	Body                      []byte
	ServiceID                 pgtype.UUID
	StartsAt                  time.Time
	Format, Locale            string
	VisitorName, VisitorEmail string
	VisitorPhone, VisitorNote string
	AdminNote                 string
	Status                    Status
	Notify                    bool
	Actor                     pgtype.UUID
}

// Created's Tasks are enqueued after commit; a replay has none.
type Created struct {
	AppointmentID pgtype.UUID
	Replayed      bool
	Tasks         []queue.Task
}

// CreateManual follows a website request's rules, except that pausing public booking does not stop it.
func CreateManual(ctx context.Context, pool *pgxpool.Pool, secret []byte, manual Manual, now time.Time) (Created, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Created{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return Created{}, err
	}
	stored, err := idempotency.Begin(ctx, q, idempotency.AdminAppointment, manual.IdempotencyKey, manual.Body, now)
	if err != nil {
		return Created{}, err
	}
	// A replay wrote nothing, so it has nothing to commit.
	if stored != nil {
		replayed := Created{
			AppointmentID: stored.ResourceID,
			Replayed:      true,
		}
		return replayed, nil
	}
	created, err := createManual(ctx, q, secret, manual, now)
	if err != nil {
		return Created{}, countConflict("manual", err)
	}
	err = idempotency.Finish(ctx, q, idempotency.AdminAppointment, manual.IdempotencyKey, created.AppointmentID,
		httpCreated, map[string]string{"id": created.AppointmentID.String()})
	if err != nil {
		return Created{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return Created{}, err
	}
	return created, nil
}

func createManual(ctx context.Context, q db.Querier, secret []byte, manual Manual, now time.Time) (Created, error) {
	current, err := settings.Load(ctx, q)
	if err != nil {
		return Created{}, err
	}
	service, err := checkManual(ctx, q, current, manual, now)
	if err != nil {
		return Created{}, err
	}
	practitionerID, err := q.GetPractitionerID(ctx)
	if err != nil {
		return Created{}, err
	}
	newAppointment := manualAppointment(practitionerID, service, current, manual, now)
	appointment, err := InsertAppointment(ctx, q, secret, newAppointment)
	if err != nil {
		return Created{}, withAlternatives(err)
	}
	appointmentsCreated.Add(newAppointment.Source, 1)
	event := Event{
		AppointmentID: appointment.ID,
		Kind:          "created",
		To:            manual.Status,
		Actor:         "admin",
		ActorUserID:   manual.Actor,
		Detail:        EventDetail{Source: "manual"},
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return Created{}, err
	}
	tasks, err := notifyManual(ctx, q, secret, appointment, current, manual.Notify, now)
	if err != nil {
		return Created{}, err
	}
	return Created{AppointmentID: appointment.ID, Tasks: tasks}, nil
}

func checkManual(ctx context.Context, q db.Querier, current settings.Settings, manual Manual,
	now time.Time) (db.Service, error) {
	service, err := GetService(ctx, q, manual.ServiceID)
	if err != nil {
		return db.Service{}, err
	}
	err = checkService(service, manual.Format)
	if err != nil {
		return db.Service{}, err
	}
	err = checkWindow(current, manual.StartsAt, now)
	if err != nil {
		return db.Service{}, err
	}
	err = checkSlot(ctx, q, current, slotRequest{Service: service, Start: manual.StartsAt}, now)
	if err != nil {
		return db.Service{}, err
	}
	return service, nil
}

func manualAppointment(practitionerID pgtype.UUID, service db.Service, current settings.Settings, manual Manual,
	now time.Time) NewAppointment {
	appointment := NewAppointment{
		PractitionerID: practitionerID,
		Service:        service,
		StartsAt:       manual.StartsAt,
		Duration:       time.Duration(service.DurationMinutes.Int32) * time.Minute,
		Status:         manual.Status,
		Timezone:       current.Timezone,
		Format:         manual.Format,
		Locale:         manual.Locale,
		Source:         "manual",
		VisitorName:    strings.TrimSpace(manual.VisitorName),
		VisitorEmail:   strings.ToLower(strings.TrimSpace(manual.VisitorEmail)),
		VisitorPhone:   optionalText(manual.VisitorPhone),
		VisitorNote:    optionalText(manual.VisitorNote),
		AdminNote:      optionalText(manual.AdminNote),
		CreatedBy:      manual.Actor,
	}
	if manual.Status == Pending {
		appointment.HoldExpiresAt = holdUntil(current, manual.StartsAt, now)
	}
	return appointment
}

func notifyManual(ctx context.Context, q db.Querier, secret []byte, appointment db.Appointment,
	current settings.Settings, notify bool, now time.Time) ([]queue.Task, error) {
	if Status(appointment.Status) == Confirmed {
		return afterConfirm(ctx, q, secret, appointment, current, now, notify)
	}
	tasks := []queue.Task{holdTask(appointment.ID, appointment.HoldExpiresAt.Time)}
	if !notify {
		return tasks, nil
	}
	received := comms.Message{
		AppointmentID: appointment.ID,
		Kind:          comms.RequestReceived,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
	}
	task, err := comms.Queue(ctx, q, received)
	if err != nil {
		return nil, err
	}
	return append(tasks, task), nil
}
