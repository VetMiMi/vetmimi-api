package comms_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

func dueIDs(t *testing.T, now time.Time) map[string]platform.Task {
	t.Helper()
	tasks, err := comms.DueTasks(context.Background(), db.New(pgtest.Pool(t)), now)
	require.NoError(t, err)
	out := map[string]platform.Task{}
	for _, task := range tasks {
		out[task.ID] = task
	}
	return out
}

func TestSweep_EnqueuesOnlyOverdueQueuedRows(t *testing.T) {
	now := time.Now()
	appt := newAppointment(t, freeStart())
	at := func(kind comms.Kind, scheduled time.Time, status string) string {
		r, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: kind, Recipient: visitorEmail, Locale: "en", ScheduledFor: scheduled})
		if status != "queued" {
			exec(t, "UPDATE communications SET status = $2, sent_at = now() WHERE id = $1", r.ID, status)
		}
		return task.ID
	}
	orphan := at(comms.RequestReceived, now.Add(-2*time.Minute), "queued")
	fresh := at(comms.RequestReceived, now.Add(-30*time.Second), "queued")
	sent := at(comms.BookingConfirmed, now.Add(-time.Hour), "sent")
	failed := at(comms.BookingConfirmed, now.Add(-time.Hour), "failed")
	cancelled := at(comms.Reminder, now.Add(-time.Hour), "cancelled")
	reminder := at(comms.Reminder, now.Add(time.Hour), "queued")

	due := dueIDs(t, now)
	require.Contains(t, due, orphan)
	require.Equal(t, comms.TaskDeliver, due[orphan].Type)
	for _, id := range []string{fresh, sent, failed, cancelled, reminder} {
		require.NotContains(t, due, id)
	}
	require.Equal(t, due, dueIDs(t, now), "a second sweep rebuilds the same task ids, which the queue keeps once")

	require.Contains(t, dueIDs(t, now.Add(time.Hour+2*time.Minute)), reminder,
		"a reminder whose task was lost is sent once it is overdue")
}

func TestRescheduleReminderRows_MovesQueuedRemindersToTheNewOffset(t *testing.T) {
	ctx := context.Background()
	appt := newAppointment(t, freeStart())
	r, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail,
		Locale: "en", ScheduledFor: appt.StartsAt.Add(-24 * time.Hour)})
	exec(t, "UPDATE settings SET value = '48' WHERE key = 'reminder_hours'")
	t.Cleanup(func() { exec(t, "UPDATE settings SET value = '24' WHERE key = 'reminder_hours'") })

	tasks, err := comms.RescheduleReminderRows(ctx, db.New(pgtest.Pool(t)))
	require.NoError(t, err)

	want := appt.StartsAt.Add(-48 * time.Hour)
	require.True(t, want.Equal(row(t, r.ID).ScheduledFor))
	var moved *platform.Task
	for i := range tasks {
		if tasks[i].ID == "comms:"+r.ID.String() {
			moved = &tasks[i]
		}
	}
	require.NotNil(t, moved)
	require.True(t, want.Equal(moved.ProcessAt))
	require.Equal(t, platform.QueueCritical, moved.Queue)
}
