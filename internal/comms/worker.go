package comms

import (
	"context"
	"encoding/json"
	"expvar"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/resend/resend-go/v2"

	"github.com/VetMiMi/vetmimi-api/internal/clock"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

const (
	TaskDeliver             = "comms:deliver"
	TaskSweep               = "comms:sweep"
	TaskRescheduleReminders = settings.TaskRescheduleReminders
)

const (
	sweepGrace = time.Minute // leaves a fresh row to the task its writer enqueued
	sweepLimit = 500
)

var emailsFailed = expvar.NewInt("emails_failed")

type deliverPayload struct {
	CommunicationID string `json:"communication_id"`
}

type Tasks struct {
	Pool          *pgxpool.Pool
	Queue         *queue.Queue
	SMTP          *SMTP          // set when EMAIL_PROVIDER is smtp
	Resend        *resend.Client // nil in development: sends are only logged
	From          string
	SiteURL       string
	SigningSecret []byte
	Log           *slog.Logger
	Now           clock.Now
}

func (t *Tasks) Register(w *queue.Worker) {
	w.Handle(TaskDeliver, t.Deliver)
	w.Handle(TaskSweep, t.Sweep)
	w.Handle(TaskRescheduleReminders, t.RescheduleReminders)
	w.Every("@every 5m", TaskSweep)
}

// Deliver is safe to repeat: the row is locked while it is sent, and only a queued row is sent.
// It returns an error only when asynq should retry.
func (t *Tasks) Deliver(ctx context.Context, payload []byte) error {
	id, err := parseDeliverPayload(payload)
	if err != nil {
		return err
	}

	result, err := t.deliver(ctx, id)
	if err != nil {
		return err
	}

	row := result.row
	switch result.status {
	case StatusCancelled:
		t.Log.InfoContext(ctx, "communication skipped",
			"kind", row.Kind,
			"communication_id", row.ID.String(),
			"skip", result.code,
		)
	case StatusSent:
		t.Log.InfoContext(ctx, "email sent", "kind", row.Kind, "communication_id", row.ID.String())
	case StatusFailed:
		emailsFailed.Add(1)
		t.Log.ErrorContext(ctx, "email failed",
			"kind", row.Kind,
			"communication_id", row.ID.String(),
			"attempts", row.Attempts,
			"error_code", result.code,
		)
	case StatusQueued:
		// An error makes asynq run the task again.
		return fmt.Errorf("comms: send failed: %s", result.code)
	}
	return nil
}

// Sweep is safe to repeat: its task ids match Queue's, so a task still waiting is not added twice.
func (t *Tasks) Sweep(ctx context.Context, _ []byte) error {
	tasks, err := DueTasks(ctx, db.New(t.Pool), t.Now())
	if err != nil {
		return err
	}

	t.Queue.Enqueue(ctx, tasks...)
	t.Log.InfoContext(ctx, "communications swept", "found", len(tasks))
	return nil
}

// RescheduleReminders is safe to repeat: it reads the stored reminder_hours each time.
func (t *Tasks) RescheduleReminders(ctx context.Context, _ []byte) error {
	tasks, err := RescheduleReminderRows(ctx, db.New(t.Pool))
	if err != nil {
		return err
	}

	// Replace, not Enqueue: asynq would otherwise keep each task's old time.
	t.Queue.Replace(ctx, tasks...)
	t.Log.InfoContext(ctx, "reminders rescheduled", "count", len(tasks))
	return nil
}

func deliverTask(id pgtype.UUID, kind Kind, at time.Time) queue.Task {
	queueName := queue.Critical
	if kind.Audience() == Practitioner {
		queueName = queue.Default
	}
	return queue.Task{
		Type:      TaskDeliver,
		Payload:   deliverPayload{CommunicationID: id.String()},
		ID:        "comms:" + id.String(),
		ProcessAt: at,
		Queue:     queueName,
	}
}

// DueTasks re-creates tasks lost between commit and enqueue. Task ids match
// Queue's, so a row whose task still exists is not enqueued twice.
func DueTasks(ctx context.Context, q db.Querier, now time.Time) ([]queue.Task, error) {
	rows, err := q.ListDueCommunications(ctx, db.ListDueCommunicationsParams{
		Before:  now.Add(-sweepGrace),
		MaxRows: sweepLimit,
	})
	if err != nil {
		return nil, err
	}

	tasks := make([]queue.Task, len(rows))
	for i, row := range rows {
		tasks[i] = deliverTask(row.ID, Kind(row.Kind), row.ScheduledFor)
	}
	return tasks, nil
}

func parseDeliverPayload(payload []byte) (pgtype.UUID, error) {
	var p deliverPayload
	var id pgtype.UUID
	err := json.Unmarshal(payload, &p)
	if err != nil {
		return id, fmt.Errorf("comms: deliver payload: %w", err)
	}
	err = id.Scan(p.CommunicationID)
	if err != nil {
		return id, fmt.Errorf("comms: deliver payload: %w", err)
	}
	return id, nil
}
