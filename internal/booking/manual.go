package booking

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// Manual is an appointment Daw Mi takes by phone or email and enters herself.
type Manual struct {
	IdempotencyKey string
	// Body is the request as the handler received it; the key's hash covers it.
	Body                      []byte
	ServiceID                 pgtype.UUID
	StartsAt                  time.Time
	Format, Locale            string
	VisitorName, VisitorEmail string
	VisitorPhone, VisitorNote string
	AdminNote                 string
	// Status is Confirmed or Pending.
	Status Status
	Notify bool
	Actor  pgtype.UUID
}

// Created is the outcome of CreateManual. Tasks are to be enqueued after it
// returns; a replay has none.
type Created struct {
	AppointmentID pgtype.UUID
	Replayed      bool
	Tasks         []platform.Task
}

// CreateManual stores an appointment Daw Mi makes by hand, under the same
// records and rules as a website request (Appointment Booking Requirements,
// "manual appointment creation"): an active service, an offered format, the
// booking window and a free slot, with no override. Pausing public booking
// does not stop her. A confirmed one gets its reminder, and its
// confirmation unless she tells the visitor herself; a pending one holds its
// slot like a website request. The same key and body again return the first
// appointment and create nothing.
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
			created, map[string]string{"id": out.AppointmentID.String()})
	})
	return out, countConflict("manual", err)
}

func createManual(ctx context.Context, q *db.Queries, secret []byte, m Manual, now time.Time) (Created, error) {
	cur, err := settings.Load(ctx, q)
	if err != nil {
		return Created{}, err
	}
	svc, err := GetService(ctx, q, m.ServiceID)
	if err != nil {
		return Created{}, err
	}
	if err := checkService(svc, m.Format); err != nil {
		return Created{}, err
	}
	if err := checkWindow(cur, m.StartsAt, now); err != nil {
		return Created{}, err
	}
	if err := checkSlot(ctx, q, cur, slotRequest{Service: svc, Start: m.StartsAt}, now); err != nil {
		return Created{}, err
	}
	practitionerID, err := q.GetPractitionerID(ctx)
	if err != nil {
		return Created{}, err
	}
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
	appt, err := InsertAppointment(ctx, q, secret, a)
	if err != nil {
		return Created{}, withAlternatives(err)
	}
	appointmentsCreated.Add(a.Source, 1)
	if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "created", To: m.Status, Actor: "admin",
		ActorUserID: m.Actor, Detail: EventDetail{Source: "manual"}}); err != nil {
		return Created{}, err
	}

	if m.Status == Confirmed {
		tasks, err := afterConfirm(ctx, q, secret, appt, cur, now, m.Notify)
		return Created{AppointmentID: appt.ID, Tasks: tasks}, err
	}
	tasks := []platform.Task{holdTask(appt.ID, appt.HoldExpiresAt.Time)}
	if m.Notify {
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestReceived,
			Recipient: appt.VisitorEmail, Locale: appt.Locale})
		if err != nil {
			return Created{}, err
		}
		tasks = append(tasks, task)
	}
	return Created{AppointmentID: appt.ID, Tasks: tasks}, nil
}
