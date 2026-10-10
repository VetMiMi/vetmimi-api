package booking_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// roomOf is the appointment's video room, or false.
func roomOf(t *testing.T, id pgtype.UUID) (db.VideoRoom, bool) {
	t.Helper()
	room, ok, err := video.RoomOf(context.Background(), db.New(pgtest.Pool(t)), id)
	require.NoError(t, err)
	return room, ok
}

func confirm(t *testing.T, appt db.Appointment, now time.Time) booking.Changed {
	t.Helper()
	changed, err := booking.Confirm(context.Background(), pgtest.Pool(t), testSecret, changeOf(t, appt), now)
	require.NoError(t, err)
	return changed
}

func TestConfirm_OnlineGetsARoomWithItsWindow(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	changed := confirm(t, appt, now)

	room, ok := roomOf(t, appt.ID)
	require.True(t, ok)
	require.Equal(t, video.StateWaiting, room.State)
	require.Equal(t, 15*time.Minute, appt.StartsAt.Sub(room.OpensAt))
	require.Equal(t, time.Hour, room.ClosesAt.Sub(appt.EndsAt))
	require.Len(t, room.JoinTokenSeed, 32)
	token := tokens.Join(testSecret, room.JoinTokenSeed)
	require.Len(t, token, 43)
	require.Equal(t, tokens.Hash(token), room.JoinTokenHash, "only the token's hash is stored")

	last := changed.Tasks[len(changed.Tasks)-1]
	require.Equal(t, video.TaskCloseRoom, last.Type)
	require.True(t, room.ClosesAt.Equal(last.ProcessAt))
}

func TestConfirm_NoRoomInManualModeOrInPerson(t *testing.T) {
	inPerson, now := booked(t, booking.Pending)
	_, err := pgtest.Pool(t).Exec(context.Background(), "UPDATE appointments SET format = 'in_person' WHERE id = $1",
		inPerson.ID)
	require.NoError(t, err)
	confirm(t, inPerson, now)
	_, ok := roomOf(t, inPerson.ID)
	require.False(t, ok, "in person")

	setSetting(t, "meeting_link_mode", `"manual_link"`)
	manual, now := booked(t, booking.Pending)
	changed := confirm(t, manual, now)
	_, ok = roomOf(t, manual.ID)
	require.False(t, ok, "manual_link mode")
	require.NotContains(t, taskTypes(changed.Tasks), video.TaskCloseRoom)
}

// ADR-004 and the unique appointment_id: two confirms at once make one room.
func TestConfirm_TwiceAtOnceMakesOneRoom(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := booking.Confirm(context.Background(), pgtest.Pool(t), testSecret, changeOf(t, appt), now)
			errs <- err
		}()
	}
	first, second := <-errs, <-errs
	if first != nil {
		first, second = second, first
	}
	require.NoError(t, first)
	requireCode(t, apperr.StaleVersion, second)
	var n int
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT count(*) FROM video_rooms WHERE appointment_id = $1", appt.ID).Scan(&n))
	require.Equal(t, 1, n)
}

func TestReschedule_MovesTheRoomKeepingItsToken(t *testing.T) {
	openEveryDay(t)
	appt, now := booked(t, booking.Pending)
	confirm(t, appt, now)
	before, _ := roomOf(t, appt.ID)

	to := appt.StartsAt.Add(4 * time.Hour)
	changed, err := reschedule(t, changeOf(t, getAppointment(t, appt.ID)), to, now)
	require.NoError(t, err)

	after, _ := roomOf(t, appt.ID)
	require.True(t, to.Add(-15*time.Minute).Equal(after.OpensAt))
	require.True(t, to.Add(2*time.Hour).Equal(after.ClosesAt))
	require.Equal(t, before.JoinTokenHash, after.JoinTokenHash, "the emailed link keeps working")
	require.Contains(t, taskTypes(changed.Tasks), video.TaskCloseRoom)
}

func TestCancel_EndsTheRoom(t *testing.T) {
	for name, cancel := range map[string]func(db.Appointment, time.Time) error{
		"admin": func(appt db.Appointment, now time.Time) error {
			_, err := booking.Cancel(context.Background(), pgtest.Pool(t), changeOf(t, appt), now)
			return err
		},
		"management link": func(appt db.Appointment, now time.Time) error {
			_, err := booking.CancelByClient(context.Background(), pgtest.Pool(t), linkOf(appt), "", now)
			return err
		},
	} {
		appt, now := booked(t, booking.Pending)
		confirm(t, appt, now)
		require.NoError(t, cancel(getAppointment(t, appt.ID), now), name)

		room, _ := roomOf(t, appt.ID)
		require.Equal(t, video.StateEnded, room.State, name)
		require.Equal(t, video.EndedByCancellation, room.EndedReason.String, name)
		require.True(t, now.Equal(room.EndedAt.Time), name)
	}
}
