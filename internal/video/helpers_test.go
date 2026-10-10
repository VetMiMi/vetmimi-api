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

var issuer = video.Issuer{Secret: []byte("ticket secret of at least 32 bytes"), PublicAPIURL: "https://api.vetmimi.example/"}

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

func roomID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	var id pgtype.UUID
	require.NoError(t, id.Scan(s))
	return id
}

func requireNotAllowed(t *testing.T, err error) {
	t.Helper()
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, apperr.ActionNotAllowed, e.Code)
}
