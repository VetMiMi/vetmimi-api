package booking

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// CancelByClient cancels the appointment a management link names, as the
// visitor (Booking & Admin UX §16). Inside the cancellation notice period it
// is recorded as late, which the visitor's email then explains. Its time
// reopens, its reminder is cancelled, its video room ends, and both the
// visitor and Daw Mi are emailed, Daw Mi with the visitor's message. It runs
// under the schedule lock, so it and an admin action on the same row take
// turns.
func CancelByClient(ctx context.Context, pool *pgxpool.Pool, token, message string, now time.Time) (Changed, error) {
	var out Changed
	err := inSchedule(ctx, pool, func(q *db.Queries) error {
		appt, err := lockByToken(ctx, q, token, now)
		if err != nil {
			return err
		}
		from := Status(appt.Status)
		if !CanTransition(from, CancelledByClient) {
			return errLinkNotAllowed
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		isLate := late(appt.StartsAt, now, time.Duration(cur.CancellationNoticeHours)*time.Hour)
		if _, err := q.CancelAppointmentByClient(ctx, db.CancelAppointmentByClientParams{
			ID: appt.ID, LateCancellation: isLate, Now: now,
		}); err != nil {
			return err
		}
		if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "cancelled", From: from,
			To: CancelledByClient, Actor: "visitor",
			Detail: EventDetail{By: "client", LateCancellation: &isLate}}); err != nil {
			return err
		}
		if err := CancelReminders(ctx, q, appt.ID, string(CancelledByClient)); err != nil {
			return err
		}
		if err := video.EndRoom(ctx, q, appt.ID, now); err != nil {
			return err
		}
		visitor, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled,
			Recipient: appt.VisitorEmail, Locale: appt.Locale})
		if err != nil {
			return err
		}
		toHer, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
			Kind: comms.PractitionerClientCancelled, Recipient: cur.ContactEmail, Text: strings.TrimSpace(message)})
		out.Tasks = []platform.Task{visitor, toHer}
		if from == Pending {
			out.Remove = []platform.Task{holdTask(appt.ID, time.Time{})}
		}
		return err
	})
	return out, err
}

// RequestReschedule records the visitor's wish to move the appointment a
// management link names, with up to three preferred starts, and tells Daw
// Mi. The appointment keeps its time until she moves it; a second request
// adds a newer entry, which is the one she is shown.
func RequestReschedule(ctx context.Context, pool *pgxpool.Pool, token string, preferred []time.Time, message string,
	now time.Time) (Changed, error) {
	var out Changed
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		appt, err := lockByToken(ctx, q, token, now)
		if err != nil {
			return err
		}
		cur, err := settings.Load(ctx, q)
		if err != nil {
			return err
		}
		utc := make([]time.Time, len(preferred))
		for i, p := range preferred {
			utc[i] = p.UTC()
		}
		if err := AppendEvent(ctx, q, Event{AppointmentID: appt.ID, Kind: "reschedule_requested", Actor: "visitor",
			Detail: EventDetail{Preferred: utc}}); err != nil {
			return err
		}
		task, err := comms.Queue(ctx, q, comms.Message{AppointmentID: appt.ID,
			Kind: comms.PractitionerRescheduleRequested, Recipient: cur.ContactEmail, Text: strings.TrimSpace(message)})
		out.Tasks = []platform.Task{task}
		return err
	})
	return out, err
}
