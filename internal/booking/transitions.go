package booking

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

// Confirm turns a pending request into a booking (docs/architecture.md,
// walkthrough 2). The row stays inside appointments_no_overlap throughout,
// so its time is never free in between. The visitor's confirmation and
// reminder are queued; a failed email never reverts it.
func Confirm(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, cur settings.Settings) (Changed, error) {
		if !appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), "confirmed")
		}
		appt, err := setStatus(ctx, q, appt, Confirmed, "confirmed", c, EventDetail{}, now)
		if err != nil {
			return Changed{}, err
		}
		tasks, err := afterConfirm(ctx, q, appt, cur, now, true)
		return Changed{Tasks: tasks, Remove: []platform.Task{holdTask(appt.ID, time.Time{})}}, err
	})
}

// Decline refuses a pending request. It leaves appointments_no_overlap, so
// its time reopens, and the visitor is told, with Daw Mi's message if she
// wrote one.
func Decline(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		appt, err := setStatus(ctx, q, appt, Declined, "declined", c, EventDetail{}, now)
		if err != nil {
			return Changed{}, err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.RequestDeclined,
			Recipient: appt.VisitorEmail, Locale: appt.Locale, ToVisitor: c.ToVisitor})
		return Changed{Tasks: []platform.Task{task}, Remove: []platform.Task{holdTask(appt.ID, time.Time{})}}, err
	})
}

// Cancel cancels a confirmed appointment that has not started, as Daw Mi
// (Booking & Admin UX §16). Pending requests are declined instead. Its time
// reopens, its reminder is cancelled, and unless Daw Mi tells the visitor
// herself they are emailed.
func Cancel(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		if !appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), "cancelled")
		}
		appt, err := setStatus(ctx, q, appt, CancelledByPractitioner, "cancelled", c,
			EventDetail{By: "practitioner"}, now)
		if err != nil {
			return Changed{}, err
		}
		if err := CancelReminders(ctx, q, appt.ID, string(CancelledByPractitioner)); err != nil {
			return Changed{}, err
		}
		if !c.Notify {
			return Changed{}, nil
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled,
			Recipient: appt.VisitorEmail, Locale: appt.Locale, ToVisitor: c.ToVisitor})
		return Changed{Tasks: []platform.Task{task}}, err
	})
}

// Complete records that a confirmed appointment took place; MarkNoShow that the
// visitor did not come. Both are operational only (Booking & Admin UX §14)
// and possible only once it has started. Nothing is sent.
func Complete(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return finish(ctx, pool, c, Completed, "completed", now)
}

// MarkNoShow is Complete's other outcome.
func MarkNoShow(ctx context.Context, pool *pgxpool.Pool, c Change, now time.Time) (Changed, error) {
	return finish(ctx, pool, c, NoShow, "no_show", now)
}

func finish(ctx context.Context, pool *pgxpool.Pool, c Change, to Status, kind string, now time.Time) (Changed, error) {
	return change(ctx, pool, c, func(q *db.Queries, appt db.Appointment, _ settings.Settings) (Changed, error) {
		if appt.StartsAt.After(now) {
			return Changed{}, invalidTransition(Status(appt.Status), kind)
		}
		if _, err := setStatus(ctx, q, appt, to, kind, c, EventDetail{}, now); err != nil {
			return Changed{}, err
		}
		return Changed{}, CancelReminders(ctx, q, appt.ID, string(to))
	})
}
