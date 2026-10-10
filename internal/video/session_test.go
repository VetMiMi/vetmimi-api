package video_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var days atomic.Int64

// withRoom books a confirmed online appointment on a day of its own, gives
// it a room and returns it with its join token.
func withRoom(t *testing.T) (db.Appointment, string) {
	t.Helper()
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	var practitioner pgtype.UUID
	err := pgtest.Pool(t).QueryRow(ctx, "SELECT id FROM users WHERE is_practitioner").Scan(&practitioner)
	if errors.Is(err, pgx.ErrNoRows) {
		practitioner, err = q.CreateUser(ctx, db.CreateUserParams{
			Email: "mi@example.com", DisplayName: "Daw Mi", PasswordHash: "x",
			Roles: []string{"site_admin"}, IsPractitioner: true, TotpSecretEnc: []byte("sealed"),
		})
	}
	require.NoError(t, err)
	svc, err := q.GetServiceBySlug(ctx, "individual-art-therapy")
	require.NoError(t, err)
	start := time.Date(2033, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, int(days.Add(1)))
	ack := sql.NullTime{Time: start.Add(-72 * time.Hour), Valid: true}
	appt, err := booking.InsertAppointment(ctx, q, issuer.Secret, booking.NewAppointment{
		PractitionerID: practitioner, Service: svc, StartsAt: start, Duration: time.Hour,
		Status: booking.Confirmed, Timezone: "Australia/Sydney", Format: "online", Locale: "my",
		Source: "website", VisitorName: "Visitor", VisitorEmail: "visitor@example.com",
		PrivacyAckAt: ack, PolicyAckAt: ack,
	})
	require.NoError(t, err)
	_, err = video.CreateRoom(ctx, q, issuer.Secret, appt, video.ModeRoom, start.Add(-72*time.Hour))
	require.NoError(t, err)
	room, _, err := video.RoomOf(ctx, q, appt.ID)
	require.NoError(t, err)
	return appt, tokens.Join(issuer.Secret, room.JoinTokenSeed)
}

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

func requireNotAllowed(t *testing.T, err error) {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, apperr.ActionNotAllowed, e.Code)
}
