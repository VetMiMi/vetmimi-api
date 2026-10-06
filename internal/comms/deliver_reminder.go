package comms

import (
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Reasons a reminder is cancelled at fire time instead of sent.
const (
	SkipNotConfirmed   = "not_confirmed"
	SkipAlreadyStarted = "already_started"
	SkipSuperseded     = "superseded"
)

// reminderSkip re-checks a reminder when it fires (ADR-006): the
// appointment must still be confirmed and in the future, and the reminder
// must be the one for its current start and reminder_hours, so a reminder a
// reschedule replaced never sends. It returns "" when the reminder may go.
func reminderSkip(appt db.GetAppointmentForMessageRow, reminderHours int, scheduledFor, now time.Time) string {
	switch {
	case appt.Status != "confirmed":
		return SkipNotConfirmed
	case !appt.StartsAt.After(now):
		return SkipAlreadyStarted
	case !appt.StartsAt.Add(-time.Duration(reminderHours) * time.Hour).Equal(scheduledFor):
		return SkipSuperseded
	}
	return ""
}
