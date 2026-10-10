package comms_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/tokens"
)

var enquiryTime = time.Date(2026, 10, 6, 1, 0, 0, 0, time.UTC)

func newEnquiry(key string) comms.Enquiry {
	return comms.Enquiry{
		IdempotencyKey: key, Body: []byte("body " + key),
		Name: " Thandar ", Email: " Thandar@Example.COM ", Organisation: "Wellbeing Co",
		EnquiryType: "workshop", Service: "workshops-programs", Subject: "A team workshop",
		Message: "We would like a workshop in November.", Locale: "en", PrivacyAcknowledged: true,
	}
}

func createEnquiry(t *testing.T, e comms.Enquiry) (comms.EnquiryCreated, error) {
	t.Helper()
	return comms.CreateEnquiry(context.Background(), pgtest.Pool(t), e, enquiryTime)
}

func requireAppErr(t *testing.T, code apperr.Code, err error) {
	t.Helper()
	var e *apperr.Error
	require.True(t, errors.As(err, &e), "%v", err)
	require.Equal(t, code, e.Code)
}

func enquiryComms(t *testing.T, id pgtype.UUID) []db.Communication {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(), "SELECT id FROM communications WHERE contact_enquiry_id = $1", id)
	require.NoError(t, err)
	var out []db.Communication
	for rows.Next() {
		var cid pgtype.UUID
		require.NoError(t, rows.Scan(&cid))
		out = append(out, row(t, cid))
	}
	require.NoError(t, rows.Err())
	return out
}

func TestEnquiry_NotifiesPractitioner(t *testing.T) {
	created, err := createEnquiry(t, newEnquiry(""))
	require.NoError(t, err)
	require.Regexp(t, `^EN-[0-9A-Z]{6}$`, created.Receipt.Reference)
	require.Equal(t, enquiryTime, created.Receipt.CreatedAt)
	require.Len(t, created.Tasks, 1)

	sent := enquiryComms(t, created.ID)
	require.Len(t, sent, 1)
	require.Equal(t, "practitioner_new_enquiry", sent[0].Kind)
	require.Equal(t, "meenaerie@gmail.com", sent[0].Recipient.String)

	resend := newFakeResend(t, http.StatusOK)
	tasks, logs := newTasks(t, resend, time.Now())
	require.NoError(t, deliver(t, tasks, created.Tasks[0]))
	email := resend.sent()[0].Body
	require.Equal(t, "New enquiry: A team workshop", email["subject"])
	require.Contains(t, email["text"], "We would like a workshop in November.")
	require.Contains(t, email["text"], "Type: Workshop / Program")
	require.Contains(t, email["text"], "https://vetmimi.example/admin/enquiries/"+created.ID.String())
	require.NotContains(t, logs.String(), "November")
	require.NotContains(t, logs.String(), "thandar")
}

func TestEnquiry_EmailStoredLowerCase(t *testing.T) {
	created, err := createEnquiry(t, newEnquiry(""))
	require.NoError(t, err)
	e, err := comms.GetEnquiry(context.Background(), db.New(pgtest.Pool(t)), created.ID)
	require.NoError(t, err)
	require.Equal(t, "thandar@example.com", e.Email)
	require.Equal(t, "Thandar", e.Name)
	require.Equal(t, "workshops-programs", e.ServiceSlug.String)
	require.Equal(t, "new", e.Status)
}

func TestEnquiry_SameKeyStoresOnce(t *testing.T) {
	key := fmt.Sprintf("enquiry-%d", time.Now().UnixNano())
	first, err := createEnquiry(t, newEnquiry(key))
	require.NoError(t, err)
	again, err := createEnquiry(t, newEnquiry(key))
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Empty(t, again.Tasks)
	require.Equal(t, first.Receipt, again.Receipt)
	require.Equal(t, first.ID, again.ID)

	other, err := createEnquiry(t, newEnquiry(""))
	require.NoError(t, err)
	require.NotEqual(t, first.ID, other.ID, "without a key every submission is new")
}

func TestEnquiry_Refusals(t *testing.T) {
	e := newEnquiry("")
	e.Service = "no-such-service"
	_, err := createEnquiry(t, e)
	requireAppErr(t, apperr.NotFound, err)

	e = newEnquiry("")
	e.PrivacyAcknowledged = false
	_, err = createEnquiry(t, e)
	requireAppErr(t, apperr.AcknowledgementRequired, err)
}

func TestEnquiry_ListAndMarkHandled(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	e := newEnquiry("")
	e.Organisation = "Unique Listing Org"
	created, err := createEnquiry(t, e)
	require.NoError(t, err)

	page, err := comms.ListEnquiries(ctx, q, comms.EnquiryFilter{Status: "new", Search: "unique listing"})
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	require.Equal(t, created.ID, page.Items[0].ID)

	by := practitionerID(t)
	handled, err := comms.MarkHandled(ctx, q, created.ID, by, enquiryTime.Add(time.Hour))
	require.NoError(t, err)
	require.Equal(t, "handled", handled.Status)
	require.Equal(t, enquiryTime.Add(time.Hour), handled.HandledAt.Time.UTC())
	require.Equal(t, by, handled.HandledBy)

	again, err := comms.MarkHandled(ctx, q, created.ID, by, enquiryTime.Add(2*time.Hour))
	require.NoError(t, err)
	require.Equal(t, handled.HandledAt, again.HandledAt, "marking twice changes nothing")

	page, err = comms.ListEnquiries(ctx, q, comms.EnquiryFilter{Status: "new", Search: "unique listing"})
	require.NoError(t, err)
	require.Empty(t, page.Items)

	_, err = comms.MarkHandled(ctx, q, pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, by, enquiryTime)
	requireAppErr(t, apperr.NotFound, err)
}

func TestEnquiry_ListPages(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	for range 3 {
		e := newEnquiry("")
		e.Subject = "Paging subject"
		_, err := createEnquiry(t, e)
		require.NoError(t, err)
	}
	first, err := comms.ListEnquiries(ctx, q, comms.EnquiryFilter{Search: "paging subject", Limit: 2})
	require.NoError(t, err)
	require.Len(t, first.Items, 2)
	require.NotEmpty(t, first.NextCursor)
	rest, err := comms.ListEnquiries(ctx, q, comms.EnquiryFilter{Search: "paging subject", Limit: 2, Cursor: first.NextCursor})
	require.NoError(t, err)
	require.Len(t, rest.Items, 1)
	require.Empty(t, rest.NextCursor)
}

func practitionerID(t *testing.T) pgtype.UUID {
	t.Helper()
	newAppointment(t, freeStart())
	var id pgtype.UUID
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), "SELECT id FROM users WHERE is_practitioner").Scan(&id))
	return id
}

// Daw Mi's email never carries the visitor's management link.
func TestDeliver_VisitorEmailCarriesManagementLink(t *testing.T) {
	appt := newAppointment(t, freeStart())
	_, visitor := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail, Locale: "my"})
	_, practitioner := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.PractitionerNewBooking, Recipient: "mi@example.com"})
	resend := newFakeResend(t, http.StatusOK)
	tasks, logs := newTasks(t, resend, time.Now())
	tasks.SigningSecret = []byte("test-signing-secret-of-32-bytes!")

	require.NoError(t, deliver(t, tasks, visitor))
	require.NoError(t, deliver(t, tasks, practitioner))
	token := tokens.Management(tasks.SigningSecret, appt.ManagementTokenSeed)
	sent := resend.sent()
	require.Contains(t, sent[0].Body["text"], "https://vetmimi.example/my/manage/"+token)
	require.NotContains(t, sent[1].Body["text"], token)
	require.NotContains(t, logs.String(), token)
}

func TestDeliver_LateCancellationWording(t *testing.T) {
	for _, late := range []bool{true, false} {
		appt := newAppointment(t, freeStart())
		exec(t, "UPDATE appointments SET status = 'cancelled_by_client', late_cancellation = $2 WHERE id = $1", appt.ID, late)
		_, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.Cancelled, Recipient: visitorEmail, Locale: "en"})
		resend := newFakeResend(t, http.StatusOK)
		tasks, _ := newTasks(t, resend, time.Now())
		require.NoError(t, deliver(t, tasks, task))
		text := resend.sent()[0].Body["text"].(string)
		if late {
			require.Contains(t, text, "counts as a late cancellation")
		} else {
			require.NotContains(t, text, "late cancellation")
		}
	}
}

func TestDeliver_RescheduleRequestShowsPreferredTimesAndMessage(t *testing.T) {
	appt := newAppointment(t, freeStart())
	preferred := time.Date(2032, 3, 2, 23, 0, 0, 0, time.UTC) // 10:00 AEDT
	exec(t, `INSERT INTO appointment_events (appointment_id, kind, actor, detail)
		VALUES ($1, 'reschedule_requested', 'visitor', jsonb_build_object('preferred', jsonb_build_array($2::text)))`,
		appt.ID, preferred.Format(time.RFC3339))
	_, task := queue(t, comms.Message{AppointmentID: appt.ID, Kind: comms.PractitionerRescheduleRequested,
		Recipient: "mi@example.com", Text: "Mornings suit me"})
	resend := newFakeResend(t, http.StatusOK)
	tasks, _ := newTasks(t, resend, time.Now())
	require.NoError(t, deliver(t, tasks, task))
	text := resend.sent()[0].Body["text"].(string)
	require.Contains(t, text, "- Wednesday 3 March 2032, 10:00 am AEDT")
	require.Contains(t, text, "Mornings suit me")
}
