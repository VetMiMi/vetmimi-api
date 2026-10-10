package video_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func TestFindSession_ShowsTheWindowAndService(t *testing.T) {
	appt, token := withRoom(t)
	s, err := video.FindSession(context.Background(), db.New(pgtest.Pool(t)), token, appt.StartsAt)
	require.NoError(t, err)
	require.Equal(t, video.Ready, s.State)
	require.True(t, appt.StartsAt.Add(-15*time.Minute).Equal(s.OpensAt))
	require.True(t, appt.EndsAt.Add(time.Hour).Equal(s.ClosesAt))
	require.Equal(t, "Australia/Sydney", s.Timezone)
	require.Equal(t, "my", s.Locale)
	require.Equal(t, "individual-art-therapy", s.ServiceSlug)
	require.Equal(t, "တစ်ဦးချင်း အနုပညာကုထုံး", s.ServiceName, "in the appointment's locale")
}

func TestFindSession_UnknownOrTamperedTokenNotFound(t *testing.T) {
	appt, token := withRoom(t)
	q := db.New(pgtest.Pool(t))
	tampered := []byte(token)
	tampered[0] ^= 1
	management := tokens.Management(issuer.Secret, appt.ManagementTokenSeed)
	for _, bad := range []string{string(tampered), management} {
		_, err := video.FindSession(context.Background(), q, bad, appt.StartsAt)
		var e *apperr.Error
		require.ErrorAs(t, err, &e)
		require.Equal(t, apperr.NotFound, e.Code)
	}
}

func TestJoinAsClient_OnlyWhileReadyAndConfirmed(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	appt, token := withRoom(t)

	_, err := video.JoinAsClient(ctx, q, issuer, token, appt.StartsAt.Add(-16*time.Minute))
	requireNotAllowed(t, err)

	ticket, err := video.JoinAsClient(ctx, q, issuer, token, appt.StartsAt)
	require.NoError(t, err)
	role, err := video.VerifyTicket(issuer.Secret, ticket.Value, ticket.RoomID, appt.StartsAt)
	require.NoError(t, err)
	require.Equal(t, video.RoleClient, role)

	_, err = pgtest.Pool(t).Exec(ctx, "UPDATE appointments SET status = 'completed' WHERE id = $1", appt.ID)
	require.NoError(t, err)
	_, err = video.JoinAsClient(ctx, q, issuer, token, appt.StartsAt)
	requireNotAllowed(t, err)

	cancelled, token := withRoom(t)
	_, err = video.EndRoom(ctx, q, cancelled.ID, video.EndedByCancellation, cancelled.StartsAt.Add(-time.Hour))
	require.NoError(t, err)
	s, err := video.FindSession(ctx, q, token, cancelled.StartsAt)
	require.NoError(t, err)
	require.Equal(t, video.Ended, s.State)
	_, err = video.JoinAsClient(ctx, q, issuer, token, cancelled.StartsAt)
	requireNotAllowed(t, err)
}

func TestPublicState_Boundaries(t *testing.T) {
	start := time.Date(2026, 11, 2, 23, 0, 0, 0, time.UTC)
	opens, closes := video.Window(start, start.Add(time.Hour))
	s := time.Second
	for name, c := range map[string]struct {
		room string
		at   time.Time
		want string
	}{
		"a second early":          {video.StateWaiting, opens.Add(-s), video.TooEarly},
		"opening":                 {video.StateWaiting, opens, video.Ready},
		"in session":              {video.StateInSession, start, video.Ready},
		"last second":             {video.StateWaiting, closes.Add(-s), video.Ready},
		"ended in the window":     {video.StateEnded, closes.Add(-s), video.Ended},
		"ended before opening":    {video.StateEnded, opens.Add(-s), video.Ended},
		"closed":                  {video.StateWaiting, closes, video.Expired},
		"expired wins over ended": {video.StateEnded, closes, video.Expired},
	} {
		require.Equal(t, c.want, video.PublicState(c.room, opens, closes, c.at), name)
	}
}

// Clocks go forward at 02:00 AEST on 4 October 2026. A session from 01:30
// AEST to 03:30 AEDT is one real hour, so its room is open 135 real
// minutes, not 195 on the wall clock.
func TestWindow_AcrossDaylightSaving(t *testing.T) {
	sydney, err := time.LoadLocation("Australia/Sydney")
	require.NoError(t, err)
	start := time.Date(2026, 10, 4, 1, 30, 0, 0, sydney)
	end := start.Add(time.Hour)
	require.Equal(t, "03:30 AEDT", end.In(sydney).Format("15:04 MST"))

	opens, closes := video.Window(start, end)
	require.Equal(t, 135*time.Minute, closes.Sub(opens))
	require.Equal(t, video.Ready, video.PublicState(video.StateWaiting, opens, closes, opens))
	require.Equal(t, video.Ready, video.PublicState(video.StateWaiting, opens, closes, closes.Add(-time.Second)))
	require.Equal(t, video.Expired, video.PublicState(video.StateWaiting, opens, closes, closes))
}
