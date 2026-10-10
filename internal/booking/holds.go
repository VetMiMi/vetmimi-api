package booking

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

const sweepHoldsLimit = 500

type holdPayload struct {
	AppointmentID string `json:"appointment_id"`
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

// holdUntil is pending_hold_hours from now, or the start if that is sooner.
func holdUntil(cur settings.Settings, start, now time.Time) sql.NullTime {
	hold := now.Add(time.Duration(cur.PendingHoldHours) * time.Hour)
	if start.Before(hold) {
		hold = start
	}
	return sql.NullTime{Time: hold, Valid: true}
}

// ExpireHold re-checks the locked row, so a confirmed request or an early or repeated task is left alone.
func ExpireHold(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) ([]queue.Task, error) {
	var tasks []queue.Task
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
		tasks = []queue.Task{task}
		return err
	})
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// SweepHolds catches overdue holds whose task Redis lost.
func SweepHolds(ctx context.Context, pool *pgxpool.Pool, now time.Time) ([]queue.Task, error) {
	ids, err := db.New(pool).ListOverdueHolds(ctx, db.ListOverdueHoldsParams{Now: now, MaxRows: sweepHoldsLimit})
	if err != nil {
		return nil, err
	}
	var tasks []queue.Task
	for _, id := range ids {
		t, err := ExpireHold(ctx, pool, id, now)
		if err != nil {
			return tasks, err
		}
		tasks = append(tasks, t...)
	}
	return tasks, nil
}
