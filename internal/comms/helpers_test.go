package comms_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	taskqueue "github.com/VetMiMi/vetmimi-api/internal/queue"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// visitorEmail must never appear in a log.
const visitorEmail = "visitor@example.com"

// days hands each appointment its own day, so fixtures never overlap.
var days atomic.Int64

func freeStart() time.Time {
	return time.Date(2032, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, int(days.Add(1)))
}

func newAppointment(t *testing.T, startsAt time.Time) db.Appointment {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.Pool(t)
	q := db.New(pool)
	var practitioner pgtype.UUID
	err := pool.QueryRow(ctx, "SELECT id FROM users WHERE is_practitioner").Scan(&practitioner)
	if errors.Is(err, pgx.ErrNoRows) {
		practitioner, err = q.CreateUser(ctx, db.CreateUserParams{
			Email: "mi@example.com", DisplayName: "Daw Mi", PasswordHash: "x",
			Roles: []string{"site_admin"}, IsPractitioner: true, TotpSecretEnc: []byte("sealed"),
		})
	}
	require.NoError(t, err)
	service, err := q.GetServiceBySlug(ctx, "individual-art-therapy")
	require.NoError(t, err)
	ack := sql.NullTime{Time: startsAt.Add(-72 * time.Hour), Valid: true}
	appt, err := booking.InsertAppointment(ctx, q, []byte("test-signing-secret-of-32-bytes!"), booking.NewAppointment{
		PractitionerID: practitioner, Service: service, StartsAt: startsAt, Duration: time.Hour,
		Status: booking.Confirmed, Timezone: "Australia/Sydney", Format: "online", Locale: "en",
		Source: "website", VisitorName: "Visitor", VisitorEmail: visitorEmail,
		PrivacyAckAt: ack, PolicyAckAt: ack,
	})
	require.NoError(t, err)
	return appt
}

func queue(t *testing.T, m comms.Message) (db.Communication, taskqueue.Task) {
	t.Helper()
	task, err := comms.Queue(context.Background(), db.New(pgtest.Pool(t)), m)
	require.NoError(t, err)
	var id pgtype.UUID
	require.NoError(t, id.Scan(task.ID[len("comms:"):]))
	return row(t, id), task
}

func row(t *testing.T, id pgtype.UUID) db.Communication {
	t.Helper()
	r, err := db.New(pgtest.Pool(t)).GetCommunication(context.Background(), id)
	require.NoError(t, err)
	return r
}

func exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	_, err := pgtest.Pool(t).Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// fakeResend answers every send with status and records the request.
type fakeResend struct {
	*httptest.Server
	status int
	delay  time.Duration

	mu       sync.Mutex
	requests []sentEmail
}

type sentEmail struct {
	IdempotencyKey string
	Body           map[string]any
}

func newFakeResend(t *testing.T, status int) *fakeResend {
	f := &fakeResend{status: status}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(body, &decoded)
		f.mu.Lock()
		f.requests = append(f.requests, sentEmail{r.Header.Get("Idempotency-Key"), decoded})
		f.mu.Unlock()
		time.Sleep(f.delay)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		if f.status == http.StatusOK {
			_, _ = w.Write([]byte(`{"id":"re_123"}`))
			return
		}
		_, _ = w.Write([]byte(`{"statusCode":500,"name":"application_error","message":"could not send to ` + visitorEmail + `"}`))
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeResend) sent() []sentEmail {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sentEmail(nil), f.requests...)
}

// newTasks only logs sends when f is nil.
func newTasks(t *testing.T, f *fakeResend, now time.Time) (*comms.Tasks, *bytes.Buffer) {
	t.Helper()
	logs := &bytes.Buffer{}
	tasks := &comms.Tasks{
		Pool: pgtest.Pool(t), From: "VetMiMi <hello@example.com>", SiteURL: "https://vetmimi.example",
		Log: slog.New(slog.NewJSONHandler(&lockedWriter{w: logs}, nil)), Now: func() time.Time { return now },
	}
	if f != nil {
		c, err := comms.NewResend("re_test_key", f.URL+"/")
		require.NoError(t, err)
		tasks.Resend = c
	}
	return tasks, logs
}

func deliver(t *testing.T, tasks *comms.Tasks, task taskqueue.Task) error {
	t.Helper()
	payload, err := json.Marshal(task.Payload)
	require.NoError(t, err)
	return tasks.Deliver(context.Background(), payload)
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
