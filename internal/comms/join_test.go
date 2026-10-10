package comms_test

import (
	"context"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func TestDeliver_JoinLinkOpensTheRoom(t *testing.T) {
	ctx := context.Background()
	secret := []byte("test-signing-secret-of-32-bytes!")
	appt := newAppointment(t, freeStart())
	q := db.New(pgtest.Pool(t))
	_, err := video.CreateRoom(ctx, q, secret, appt, video.ModeRoom, time.Now())
	require.NoError(t, err)

	_, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail,
		Locale: "en"})
	resend := newFakeResend(t, http.StatusOK)
	tasks, logs := newTasks(t, resend, time.Now())
	tasks.SigningSecret = secret
	require.NoError(t, deliver(t, tasks, task))

	sent := resend.sent()
	require.Len(t, sent, 1)
	link := regexp.MustCompile(`https://vetmimi\.example/session/([A-Za-z0-9_-]{43})\n`)
	m := link.FindStringSubmatch(sent[0].Body["text"].(string))
	require.NotNil(t, m, "the plain-text part has the link")
	require.Contains(t, sent[0].Body["html"], m[0][:len(m[0])-1], "so has the HTML part")

	s, err := video.FindSession(ctx, q, m[1], appt.StartsAt)
	require.NoError(t, err)
	require.Equal(t, video.Ready, s.State)
	require.NotContains(t, logs.String(), "/session/")
	require.NotContains(t, logs.String(), m[1])
}
