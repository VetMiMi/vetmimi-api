package booking

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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

// Tasks runs the booking background jobs.
type Tasks struct {
	Pool     *pgxpool.Pool
	Queue    *queue.Queue
	Log      *slog.Logger
	Now      clock.Now
	Timezone string
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskExpireHold, t.expireHold)
	w.Handle(TaskSweepHolds, t.sweepHolds)
	w.Handle(TaskPurgeRetention, t.purgeRetention)
	w.Every("@every 5m", TaskSweepHolds)
	// CRON_TZ keeps the purge at 03:00 practice time across daylight saving.
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

func (t *Tasks) purgeRetention(ctx context.Context, _ []byte) error {
	purged, err := Purge(ctx, db.New(t.Pool), t.Now())
	t.Log.InfoContext(ctx, "retention purged",
		"appointments_deleted", purged.Appointments, "enquiries_deleted", purged.Enquiries)
	return err
}
