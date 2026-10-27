package comms

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

var (
	errMessageNotFound     = apperr.New(apperr.NotFound, "This appointment has no message with this id.")
	errAppointmentNotFound = apperr.New(apperr.NotFound, "No appointment has this id.")
	errNotResendable       = apperr.New(apperr.ActionNotAllowed, "Only an email that was sent or failed can be sent again.")
	errReminderNotDue      = apperr.New(apperr.ActionNotAllowed, "A reminder can be sent again only before a confirmed appointment.")
)

// Resend queues a copy of a sent or failed email and leaves the original as it
// was. A visitor's copy goes to their current address, in case it was corrected.
func Resend(ctx context.Context, pool *pgxpool.Pool, appointmentID, id, actor pgtype.UUID,
	now time.Time) (db.Communication, queue.Task, error) {
	var row db.Communication
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		orig, err := q.GetCommunication(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && orig.AppointmentID != appointmentID {
			return errMessageNotFound
		}
		if err != nil {
			return err
		}
		if orig.Channel != "email" || (Status(orig.Status) != StatusSent && Status(orig.Status) != StatusFailed) {
			return errNotResendable
		}
		appt, err := q.GetAppointmentForMessage(ctx, appointmentID)
		if err != nil {
			return err
		}
		if Kind(orig.Kind) == Reminder && (appt.Status != "confirmed" || !appt.StartsAt.After(now)) {
			return errReminderNotDue
		}
		recipient := orig.Recipient.String
		if Audience(orig.Audience) == Visitor {
			recipient = appt.VisitorEmail
		}
		row, err = q.InsertCommunication(ctx, db.InsertCommunicationParams{
			AppointmentID: orig.AppointmentID,
			Kind:          orig.Kind,
			Audience:      orig.Audience,
			Recipient:     pgtype.Text{String: strings.ToLower(recipient), Valid: true},
			Locale:        orig.Locale,
			ScheduledFor:  sql.NullTime{Time: now, Valid: true},
			Message:       orig.Message,
			ResendOf:      orig.ID,
			CreatedBy:     actor,
		})
		return err
	})
	if err != nil {
		return db.Communication{}, queue.Task{}, err
	}
	return row, deliverTask(row.ID, Kind(row.Kind), row.ScheduledFor), nil
}

// MarkCommunicated records that Daw Mi told the visitor herself, for example by
// phone; it clears a failed email of the same kind from the dashboard.
func MarkCommunicated(ctx context.Context, q db.Querier, appointmentID pgtype.UUID, kind Kind, note string,
	actor pgtype.UUID, now time.Time) (db.Communication, error) {
	appt, err := q.GetAppointmentForMessage(ctx, appointmentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Communication{}, errAppointmentNotFound
	}
	if err != nil {
		return db.Communication{}, err
	}
	return q.InsertManualCommunication(ctx, db.InsertManualCommunicationParams{
		AppointmentID: appointmentID,
		Kind:          string(kind),
		Locale:        appt.Locale,
		SentAt:        now,
		CreatedBy:     actor,
		Note:          pgtype.Text{String: note, Valid: true},
	})
}
