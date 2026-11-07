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

// ExpireHold re-checks the locked row, so a confirmed request or an early or repeated task is left alone.
func ExpireHold(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, now time.Time) ([]queue.Task, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	appointment, err := q.LockAppointment(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !CanTransition(Status(appointment.Status), Expired) || !appointment.HoldExpiresAt.Valid ||
		appointment.HoldExpiresAt.Time.After(now) {
		return nil, nil
	}
	params := db.SetAppointmentStatusParams{
		ID:     id,
		Status: string(Expired),
		Now:    now,
	}
	_, err = q.SetAppointmentStatus(ctx, params)
	if err != nil {
		return nil, err
	}
	event := Event{
		AppointmentID: id,
		Kind:          "expired",
		From:          Pending,
		To:            Expired,
		Actor:         "system",
	}
	err = AppendEvent(ctx, q, event)
	if err != nil {
		return nil, err
	}
	err = CancelReminders(ctx, q, id, string(Expired))
	if err != nil {
		return nil, err
	}
	message := comms.Message{
		AppointmentID: id,
		Kind:          comms.RequestExpired,
		Recipient:     appointment.VisitorEmail,
		Locale:        appointment.Locale,
	}
	task, err := comms.Queue(ctx, q, message)
	if err != nil {
		return nil, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return nil, err
	}
	return []queue.Task{task}, nil
}

// holdUntil is pending_hold_hours from now, or the start if that is sooner.
func holdUntil(current settings.Settings, start, now time.Time) sql.NullTime {
	hold := now.Add(time.Duration(current.PendingHoldHours) * time.Hour)
	if start.Before(hold) {
		hold = start
	}
	return sql.NullTime{Time: hold, Valid: true}
}
