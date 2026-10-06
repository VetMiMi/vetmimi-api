package booking

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/clock"
)

// Task types this package handles (docs/architecture.md, "Background jobs").
const (
	TaskExpireHold = "booking:expire-hold"
	TaskSweepHolds = "booking:sweep-holds"
)

// sweepHoldsLimit bounds one sweep; the next takes the rest.
const sweepHoldsLimit = 500

type holdPayload struct {
	AppointmentID string `json:"appointment_id"`
}

// holdTask expires appointment id's hold at at. Its id makes enqueueing it
// twice harmless.
func holdTask(id pgtype.UUID, at time.Time) platform.Task {
	return platform.Task{
		Type:      TaskExpireHold,
		Payload:   holdPayload{AppointmentID: id.String()},
		ID:        "hold:" + id.String(),
		ProcessAt: at,
	}
}

// ExpireHold expires a pending request whose hold has passed (ADR-004): the
// status becomes expired, which takes it out of appointments_no_overlap and
// so reopens the slot, and the visitor is told. It locks the row and
// re-checks it, so a request confirmed meanwhile, or a task that fires early
// or twice, changes nothing. It returns the tasks to enqueue after commit.
func ExpireHold(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) ([]platform.Task, error) {
	var tasks []platform.Task
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		appt, err := q.LockAppointment(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !CanTransition(Status(appt.Status), Expired) || !appt.HoldExpiresAt.Valid ||
			appt.HoldExpiresAt.Time.After(now) {
			return nil
		}
		if _, err := q.SetAppointmentStatus(ctx, db.SetAppointmentStatusParams{
			ID: id, Status: string(Expired), Now: now,
		}); err != nil {
			return err
		}
		if err := AppendEvent(ctx, q, Event{AppointmentID: id, Kind: "expired", From: Pending, To: Expired,
			Actor: "system"}); err != nil {
			return err
		}
		if err := CancelReminders(ctx, q, id, string(Expired)); err != nil {
			return err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: id, Kind: comms.RequestExpired,
			Recipient: appt.VisitorEmail, Locale: appt.Locale})
		tasks = []platform.Task{task}
		return err
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// SweepHolds expires every overdue hold through ExpireHold, for the ones
// whose task Redis lost, and returns the tasks to enqueue.
func SweepHolds(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]platform.Task, error) {
	ids, err := db.New(pool).ListOverdueHolds(ctx, db.ListOverdueHoldsParams{Now: now, MaxRows: sweepHoldsLimit})
	if err != nil {
		return nil, err
	}
	var tasks []platform.Task
	for _, id := range ids {
		t, err := ExpireHold(ctx, pool, id, now)
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, t...)
	}
	return tasks, nil
}

// Tasks runs the booking task handlers in the worker. Timezone is the
// practice's, as settings held it when the worker started.
type Tasks struct {
	Pool     *pgxpool.Pool
	Queue    *platform.Queue
	Log      *slog.Logger
	Now      clock.Now
	Timezone string
}

// Register adds the hold and retention handlers and their schedules to w.
// The purge runs at 03:00 practice time, which CRON_TZ keeps at 03:00 across
// daylight saving.
func (t *Tasks) Register(w *platform.Worker) {
	w.Handle(TaskExpireHold, t.expireHold)
	w.Handle(TaskSweepHolds, t.sweepHolds)
	w.Handle(TaskPurgeRetention, t.purgeRetention)
	w.Every("@every 5m", TaskSweepHolds)
	w.Every("CRON_TZ="+t.Timezone+" 0 3 * * *", TaskPurgeRetention)
}

func (t *Tasks) expireHold(ctx context.Context, payload []byte) error {
	var p holdPayload
	var id pgtype.UUID
	if err := json.Unmarshal(payload, &p); err != nil {
		return fmt.Errorf("booking: expire-hold payload: %w", err)
	}
	if err := id.Scan(p.AppointmentID); err != nil {
		return fmt.Errorf("booking: expire-hold payload: %w", err)
	}
	tasks, err := ExpireHold(ctx, t.Pool, id, t.Now())
	t.Queue.Enqueue(ctx, tasks...)
	return err
}

func (t *Tasks) sweepHolds(ctx context.Context, _ []byte) error {
	tasks, err := SweepHolds(ctx, t.Pool, t.Now())
	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "holds swept", "expired", len(tasks))
	return err
}
