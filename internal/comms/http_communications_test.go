package comms_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_ListCommunications(t *testing.T) {
	first := newAppointment(t, freeStart())
	second := newAppointment(t, freeStart())
	exec(t, "UPDATE appointments SET visitor_name = 'Thiri' WHERE id = $1", second.ID)
	// Due after anything the other tests queue, so these come first.
	due := time.Date(2040, 1, 1, 9, 0, 0, 0, time.UTC)
	confirmed, _ := queue(t, comms.Message{AppointmentID: first.ID, Kind: comms.BookingConfirmed, Recipient: visitorEmail,
		Locale: "en", ScheduledFor: due})
	failed, _ := queue(t, comms.Message{AppointmentID: second.ID, Kind: comms.RequestReceived, Recipient: visitorEmail,
		Locale: "en", ScheduledFor: due.Add(time.Hour)})
	fail(t, failed.ID)
	reminder, _ := queue(t, comms.Message{AppointmentID: first.ID, Kind: comms.Reminder, Recipient: visitorEmail,
		Locale: "en", ScheduledFor: due.Add(2 * time.Hour)})
	_, err := comms.MarkCommunicated(context.Background(), db.New(pgtest.Pool(t)), first.ID, comms.BookingConfirmed,
		"Phoned", practitioner(t), due.Add(3*time.Hour))
	require.NoError(t, err)
	a := commsAPI(t)
	token := webtest.SignIn(t, "booking_admin")

	page := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/communications?limit=2", token, ""))
	require.Equal(t, []string{reminder.ID.String(), failed.ID.String()}, itemIDs(page))
	item := page["items"].([]any)[1].(map[string]any)
	require.Equal(t, second.ID.String(), item["appointmentId"])
	require.Equal(t, second.Reference, item["appointmentReference"])
	require.Equal(t, "Thiri", item["visitorName"])
	require.Equal(t, "failed", item["status"])

	next := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet,
		"/admin/communications?limit=2&cursor="+page["nextCursor"].(string), token, ""))
	require.Equal(t, confirmed.ID.String(), itemIDs(next)[0])

	failures := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/communications?status=failed", token, ""))
	require.Equal(t, failed.ID.String(), itemIDs(failures)[0])
	for _, item := range failures["items"].([]any) {
		require.Equal(t, "failed", item.(map[string]any)["status"])
	}

	found := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/communications?q="+first.Reference, token, ""))
	require.Equal(t, []string{reminder.ID.String(), confirmed.ID.String()}, itemIDs(found), "the phone call is not an email")

	require.NotContains(t, a.Logs.String(), "Thiri")
	require.NotContains(t, a.Logs.String(), visitorEmail)
}

func TestHTTP_ListCommunicationsRefusals(t *testing.T) {
	a := commsAPI(t)
	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodGet, "/admin/communications", webtest.SignIn(t, "content_editor"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/communications?status=delivered", webtest.SignIn(t, "booking_admin"), ""))
	require.Equal(t, "status", p["errors"].([]any)[0].(map[string]any)["field"])
}

func itemIDs(page map[string]any) []string {
	var ids []string
	for _, item := range page["items"].([]any) {
		ids = append(ids, item.(map[string]any)["id"].(string))
	}
	return ids
}
