package booking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

const (
	TaskExpireHold     = "booking:expire-hold"
	TaskSweepHolds     = "booking:sweep-holds"
	TaskPurgeRetention = "booking:purge-retention"
)

const sweepHoldsLimit = 500

type holdPayload struct {
	AppointmentID string `json:"appointment_id"`
}

// Tasks runs the booking background jobs.
type Tasks struct {
	Pool     *pgxpool.Pool
	Queue    *queue.Queue
	Log      *slog.Logger
	Now      clock.Now
	Timezone string
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskExpireHold, t.ExpireHold)
	w.Handle(TaskSweepHolds, t.SweepHolds)
	w.Handle(TaskPurgeRetention, t.PurgeRetention)
	w.Every("@every 5m", TaskSweepHolds)
	// CRON_TZ keeps the purge at 03:00 practice time across daylight saving.
	w.Every("CRON_TZ="+t.Timezone+" 0 3 * * *", TaskPurgeRetention)
}

// ExpireHold is safe to repeat: it re-checks the locked row, so an expired or confirmed request is left alone.
func (t *Tasks) ExpireHold(ctx context.Context, payload []byte) error {
	var p holdPayload
	err := json.Unmarshal(payload, &p)
	if err != nil {
		return fmt.Errorf("booking: expire-hold payload: %w", err)
	}
	var appointmentID pgtype.UUID
	err = appointmentID.Scan(p.AppointmentID)
	if err != nil {
		return fmt.Errorf("booking: expire-hold payload: %w", err)
	}

	tasks, err := ExpireHold(ctx, t.Pool, appointmentID, t.Now())
	if err != nil {
		return err
	}

	t.Queue.Enqueue(ctx, tasks...)
	return nil
}

// SweepHolds is safe to repeat: each hold it finds is expired by ExpireHold, which re-checks its row.
func (t *Tasks) SweepHolds(ctx context.Context, _ []byte) error {
	tasks, err := SweepHolds(ctx, t.Pool, t.Now())
	// Holds expired before a failure are committed, so their emails are queued anyway.
	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "holds swept", "expired", len(tasks))
	return err
}

// PurgeRetention is safe to repeat: a second run finds nothing older than the retention period left.
func (t *Tasks) PurgeRetention(ctx context.Context, _ []byte) error {
	purged, err := Purge(ctx, db.New(t.Pool), t.Now())
	// Batches deleted before a failure are gone, so they are logged anyway.
	t.Log.InfoContext(ctx, "retention purged",
		"appointments_deleted", purged.Appointments,
		"enquiries_deleted", purged.Enquiries,
	)
	return err
}

// holdTask's fixed id makes enqueueing it twice harmless.
func holdTask(id pgtype.UUID, at time.Time) queue.Task {
	return queue.Task{
		Type:      TaskExpireHold,
		Payload:   holdPayload{AppointmentID: id.String()},
		ID:        "hold:" + id.String(),
		ProcessAt: at,
	}
}

// SweepHolds catches overdue holds whose task Redis lost.
func SweepHolds(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]queue.Task, error) {
	params := db.ListOverdueHoldsParams{
		Now:     now,
		MaxRows: sweepHoldsLimit,
	}
	ids, err := db.New(pool).ListOverdueHolds(ctx, params)
	if err != nil {
		return nil, err
	}
	var tasks []queue.Task
	for _, id := range ids {
		expired, err := ExpireHold(ctx, pool, id, now)
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, expired...)
	}
	return tasks, nil
}
