package video_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/video"
)

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
