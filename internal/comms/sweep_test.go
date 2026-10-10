package comms_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	taskqueue "github.com/VetMiMi/vetmimi-api/internal/queue"
)

func dueIDs(t *testing.T, now time.Time) map[string]taskqueue.Task {
	t.Helper()
	tasks, err := comms.DueTasks(context.Background(), db.New(pgtest.Pool(t)), now)
	require.NoError(t, err)
	out := map[string]taskqueue.Task{}
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
	var moved *taskqueue.Task
	for i := range tasks {
		if tasks[i].ID == "comms:"+r.ID.String() {
			moved = &tasks[i]
		}
	}
	require.NotNil(t, moved)
	require.True(t, want.Equal(moved.ProcessAt))
	require.Equal(t, taskqueue.Critical, moved.Queue)
}

func TestReminderRescheduleTasks_OnlyWhenReminderHoursChanges(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	require.Empty(t, comms.ReminderRescheduleTasks(settings.Patch{"pending_hold_hours": []byte("12")}, now))

	tasks := comms.ReminderRescheduleTasks(settings.Patch{"reminder_hours": []byte("48")}, now)
	require.Len(t, tasks, 1)
	require.Equal(t, comms.TaskRescheduleReminders, tasks[0].Type)
	require.Equal(t, "reminders:1791709200000000000", tasks[0].ID)
}
