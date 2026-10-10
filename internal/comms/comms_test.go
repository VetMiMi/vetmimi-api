package comms_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	taskqueue "github.com/VetMiMi/vetmimi-api/internal/queue"
)

func TestQueue_RowCommitsWithTheTransactionAndReturnsItsTask(t *testing.T) {
	ctx := context.Background()
	appt := newAppointment(t, freeStart())
	tx, err := pgtest.Pool(t).Begin(ctx)
	require.NoError(t, err)
	task, err := comms.Queue(ctx, db.New(tx), comms.Message{
		AppointmentID: appt.ID, Kind: comms.RequestReceived, Recipient: "Visitor@Example.COM", Locale: "my",
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	var id pgtype.UUID
	require.NoError(t, id.Scan(task.ID[len("comms:"):]))
	got := row(t, id)
	require.Equal(t, "queued", got.Status)
	require.Equal(t, "visitor", got.Audience)
	require.Equal(t, "email", got.Channel)
	require.Equal(t, "visitor@example.com", got.Recipient.String, "stored lower-case")
	require.Equal(t, "my", got.Locale)
	require.Equal(t, comms.TaskDeliver, task.Type)
	require.Equal(t, taskqueue.Critical, task.Queue)
	require.Equal(t, got.ScheduledFor, task.ProcessAt)
	payload, err := json.Marshal(task.Payload)
	require.NoError(t, err)
	require.JSONEq(t, `{"communication_id":"`+id.String()+`"}`, string(payload), "the task carries the id only")

	practitioner, task := queue(t, comms.Message{
		AppointmentID: appt.ID, Kind: comms.PractitionerNewRequest, Recipient: "mi@example.com", Locale: "my",
	})
	require.Equal(t, "practitioner", practitioner.Audience)
	require.Equal(t, "en", practitioner.Locale, "Daw Mi's messages are in English for now")
	require.Equal(t, taskqueue.Default, task.Queue)
}

func requireCheck(t *testing.T, constraint string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, constraint, pgErr.ConstraintName)
}

func TestCommunications_ExactlyOneParent(t *testing.T) {
	appt := newAppointment(t, freeStart())
	insert := "INSERT INTO communications (appointment_id, contact_enquiry_id, kind, audience, recipient, locale) " +
		"VALUES ($1, $2, 'request_received', 'visitor', 'v@example.com', 'en')"
	_, err := pgtest.Pool(t).Exec(context.Background(), insert, nil, nil)
	requireCheck(t, "communications_one_parent", err)
	_, err = pgtest.Pool(t).Exec(context.Background(), insert, appt.ID, appt.ID)
	requireCheck(t, "communications_one_parent", err)
}

func TestCommunications_EmailNeedsRecipient(t *testing.T) {
	appt := newAppointment(t, freeStart())
	_, err := pgtest.Pool(t).Exec(context.Background(),
		"INSERT INTO communications (appointment_id, kind, audience, locale) VALUES ($1, 'reminder', 'visitor', 'en')",
		appt.ID)
	requireCheck(t, "communications_email_has_recipient", err)
}

func TestCancel_QueuedOnly(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	appt := newAppointment(t, freeStart())
	queued, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Reminder, Recipient: visitorEmail, Locale: "en"})
	sent, _ := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "en"})
	exec(t, "UPDATE communications SET status = 'sent', sent_at = now() WHERE id = $1", sent.ID)

	cancelled, err := comms.Cancel(ctx, q, queued.ID, comms.SkipSuperseded)
	require.NoError(t, err)
	require.True(t, cancelled)
	cancelled, err = comms.Cancel(ctx, q, sent.ID, comms.SkipSuperseded)
	require.NoError(t, err)
	require.False(t, cancelled, "a sent message cannot be cancelled")
	cancelled, err = comms.Cancel(ctx, q, appt.ID, comms.SkipSuperseded)
	require.NoError(t, err)
	require.False(t, cancelled, "no communication has this id")

	require.Equal(t, "cancelled", row(t, queued.ID).Status)
	require.Equal(t, comms.SkipSuperseded, row(t, queued.ID).Error.String)
	require.Equal(t, "sent", row(t, sent.ID).Status, "a sent message stays sent")
	require.False(t, row(t, sent.ID).Error.Valid)
}

func TestCanTransition(t *testing.T) {
	for _, to := range []comms.Status{comms.StatusSent, comms.StatusFailed, comms.StatusCancelled} {
		require.True(t, comms.CanTransition(comms.StatusQueued, to), to)
	}
	for _, from := range []comms.Status{comms.StatusSent, comms.StatusFailed, comms.StatusCancelled} {
		for _, to := range comms.Statuses {
			require.False(t, comms.CanTransition(from, to), "%s → %s", from, to)
		}
	}
}

// The Go lists and the contract's enums cannot drift apart.
func TestKindsAndStatusesMatchTheContract(t *testing.T) {
	spec, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	require.NoError(t, err)
	enum := func(name string) []string {
		var out []string
		for _, v := range spec.Components.Schemas[name].Value.Enum {
			out = append(out, v.(string))
		}
		slices.Sort(out)
		return out
	}
	var kinds, statuses []string
	for _, k := range comms.Kinds {
		kinds = append(kinds, string(k))
	}
	for _, s := range comms.Statuses {
		statuses = append(statuses, string(s))
	}
	slices.Sort(kinds)
	slices.Sort(statuses)
	require.Equal(t, enum("CommunicationKind"), kinds)
	require.Equal(t, enum("CommunicationStatus"), statuses)
}
