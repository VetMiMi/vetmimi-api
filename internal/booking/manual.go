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
func CreateManual(ctx context.Context, pool *pgxpool.Pool, secret []byte, m Manual, now time.Time) (Created, error) {
	var out Created
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		stored, err := idempotency.Begin(ctx, q, idempotency.AdminAppointment, m.IdempotencyKey, m.Body, now)
		if err != nil {
			return err
		}
		if stored != nil {
			out = Created{AppointmentID: stored.ResourceID, Replayed: true}
			return nil
		}
		out, err = createManual(ctx, q, secret, m, now)
		if err != nil {
			return err
		}
		return idempotency.Finish(ctx, q, idempotency.AdminAppointment, m.IdempotencyKey, out.AppointmentID,
			httpCreated, map[string]string{"id": out.AppointmentID.String()})
	})
	return out, countConflict("manual", err)
}

func createManual(ctx context.Context, q *db.Queries, secret []byte, m Manual, now time.Time) (Created, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Created{}, err
	}
	svc, err := checkManual(ctx, q, cur, m, now)
	if err != nil {
		return Created{}, err
	}
	practitionerID, err := q.GetPractitionerID(ctx)
	if err != nil {
		return Created{}, err
	}
	a := manualAppointment(practitionerID, svc, cur, m, now)
	appt, err := InsertAppointment(ctx, q, secret, a)
	if err != nil {
		return Created{}, withAlternatives(err)
	}
	appointmentsCreated.Add(a.Source, 1)
	if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "created", To: m.Status, Actor: "admin",
		ActorUserID: m.Actor, Detail: EventDetail{Source: "manual"}}); err != nil {
		return Created{}, err
	}
	tasks, err := notifyManual(ctx, q, secret, appt, cur, m.Notify, now)
	return Created{AppointmentID: appt.ID, Tasks: tasks}, err
}

func checkManual(ctx context.Context, q db.Querier, cur settings.Settings, m Manual, now time.Time) (db.Service, error) {
	svc, err := GetService(ctx, q, m.ServiceID)
	if err != nil {
		return db.Service{}, err
	}
	if err := checkService(svc, m.Format); err != nil {
		return db.Service{}, err
	}
	if err := checkWindow(cur, m.StartsAt, now); err != nil {
		return db.Service{}, err
	}
	return svc, checkSlot(ctx, q, cur, slotRequest{Service: svc, Start: m.StartsAt}, now)
}

func manualAppointment(practitionerID pgtype.UUID, svc db.Service, cur settings.Settings, m Manual,
	now time.Time) NewAppointment {
	a := NewAppointment{
		PractitionerID: practitionerID,
		Service:        svc,
		StartsAt:       m.StartsAt,
		Duration:       time.Duration(svc.DurationMinutes.Int32) * time.Minute,
		Status:         m.Status,
		Timezone:       cur.Timezone,
		Format:         m.Format,
		Locale:         m.Locale,
		Source:         "manual",
		VisitorName:    strings.TrimSpace(m.VisitorName),
		VisitorEmail:   strings.ToLower(strings.TrimSpace(m.VisitorEmail)),
		VisitorPhone:   optionalText(m.VisitorPhone),
		VisitorNote:    optionalText(m.VisitorNote),
		AdminNote:      optionalText(m.AdminNote),
		CreatedBy:      m.Actor,
	}
	if m.Status == Pending {
		a.HoldExpiresAt = holdUntil(cur, m.StartsAt, now)
	}
	return a
}

func notifyManual(ctx context.Context, q db.Querier, secret []byte, appt db.Appointment, cur settings.Settings,
	notify bool, now time.Time) ([]queue.Task, error) {
	if Status(appt.Status) == Confirmed {
		return afterConfirm(ctx, q, secret, appt, cur, now, notify)
	}
	tasks := []queue.Task{holdTask(appt.ID, appt.HoldExpiresAt.Time)}
	if !notify {
		return tasks, nil
	}
	task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived,
		Recipient: appt.VisitorEmail, Locale: appt.Locale})
	if err != nil {
		return nil, err
	}
	return append(tasks, task), nil
}
