package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

// joinLink books a confirmed online appointment at startsAt with a room and
// returns its join token, derived as the worker derives it for the email.
func joinLink(t *testing.T, startsAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	svc, err := q.GetServiceBySlug(ctx, "individual-art-therapy")
	require.NoError(t, err)
	appt := bookOnce(t, svc.ID.String(), startsAt, booking.Confirmed)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM appointments WHERE id = $1", appt.ID)
		require.NoError(t, err)
	})
	secret := []byte("test signing secret, 32 bytes ok")
	_, err = video.CreateRoom(ctx, q, secret, appt, video.ModeRoom, startsAt.Add(-time.Hour))
	require.NoError(t, err)
	room, _, err := video.RoomOf(ctx, q, appt.ID)
	require.NoError(t, err)
	return platform.NewJoinToken(secret, room.JoinTokenSeed)
}

func TestPublicSession_ReadyGivesAClientTicket(t *testing.T) {
	a := newAuthAPI(t)
	start := a.clock.at.Add(5 * time.Minute).Truncate(time.Minute).UTC()
	token := joinLink(t, start)

	got := decoded(t, http.StatusOK, a.public("/public/sessions/"+token))
	require.Equal(t, "ready", got["state"])
	require.Equal(t, start.Add(-15*time.Minute).Format(time.RFC3339), got["opensAt"])
	require.Equal(t, start.Add(2*time.Hour).Format(time.RFC3339), got["closesAt"])
	require.Equal(t, "Australia/Sydney", got["timezone"])
	require.Equal(t, map[string]any{"slug": "individual-art-therapy", "name": "Individual Art Therapy"}, got["service"])
	require.Equal(t, []string{"closesAt", "endsAt", "locale", "opensAt", "service", "startsAt", "state", "timezone"},
		keys(got), "no visitor details")

	ticket := decoded(t, http.StatusCreated, a.sendPublic(http.MethodPost, "/public/sessions/"+token+"/ticket", ""))
	require.Equal(t, "client", ticket["role"])
	require.Equal(t, a.clock.at.Add(5*time.Minute).UTC().Truncate(time.Second).Format(time.RFC3339),
		ticket["ticketExpiresAt"])
	require.Equal(t, "wss://api.vetmimi.example/video/rooms/"+ticket["roomId"].(string)+"/ws", ticket["websocketUrl"])
	require.Equal(t, []any{map[string]any{"urls": []any{"stun:stun.l.google.com:19302"}}}, ticket["iceServers"],
		"STUN only without TURN_HOST")
	var room pgtype.UUID
	require.NoError(t, room.Scan(ticket["roomId"]))
	role, err := video.VerifyTicket([]byte("test signing secret, 32 bytes ok"), ticket["ticket"].(string), room,
		a.clock.at)
	require.NoError(t, err)
	require.Equal(t, "client", role)

	require.NotContains(t, a.logs.String(), token)
	require.NotContains(t, a.logs.String(), ticket["ticket"])
}

func TestPublicSession_Refusals(t *testing.T) {
	a := newAuthAPI(t)
	early := joinLink(t, a.clock.at.Add(2*time.Hour).Truncate(time.Minute))
	require.Equal(t, "too_early", decoded(t, http.StatusOK, a.public("/public/sessions/"+early))["state"])
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.sendPublic(http.MethodPost, "/public/sessions/"+early+"/ticket", ""))

	unknown := refused(t, http.StatusNotFound, "not_found", a.public("/public/sessions/"+strings.Repeat("Z", 43)))
	manage := refused(t, http.StatusNotFound, "not_found", a.public("/public/manage/"+strings.Repeat("Z", 43)))
	require.Equal(t, manage["detail"], unknown["detail"], "a join link's 404 reads like any other link's")
	refused(t, http.StatusNotFound, "not_found",
		a.sendPublic(http.MethodPost, "/public/sessions/"+strings.Repeat("Z", 43)+"/ticket", ""))
	refused(t, http.StatusBadRequest, "invalid_request", a.public("/public/sessions/"+strings.Repeat("Z", 42)))
	requireUnauthenticated(t, a.send(http.MethodGet, "/public/sessions/"+early, "not-a-service-key"))

	token := insertSession(t, a.clock.at, "booking_admin")
	path := appointmentsURL + "/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10/video-session"
	refused(t, http.StatusNotImplemented, "not_implemented", a.sendJSON(http.MethodPost, path, token, ""))
	refused(t, http.StatusNotImplemented, "not_implemented", a.sendJSON(http.MethodPost, path+"/end", token, ""))
}
