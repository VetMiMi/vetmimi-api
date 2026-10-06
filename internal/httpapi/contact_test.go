package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func enquiryBody(service, message string) string {
	return fmt.Sprintf(`{"name": "Thandar", "email": "Thandar@Example.com", "enquiryType": "workshop",
		"service": %q, "subject": "Team workshop", "message": %q, "locale": "en", "privacyAcknowledged": true}`,
		service, message)
}

func TestContactEnquiries(t *testing.T) {
	a := newAuthAPI(t)
	const key = "8a0e7c1d-2b3f-4e5a-9c6d-7e8f9a0b1c2d"
	res := a.sendPublic(http.MethodPost, "/public/contact-enquiries", enquiryBody("workshops-programs", "Hello there"),
		"Idempotency-Key", key)
	receipt := decoded(t, http.StatusCreated, res)
	require.Equal(t, []string{"createdAt", "reference"}, keys(receipt))
	require.Regexp(t, `^EN-`, receipt["reference"])

	replay := a.sendPublic(http.MethodPost, "/public/contact-enquiries", enquiryBody("workshops-programs", "Hello there"),
		"Idempotency-Key", key)
	require.Equal(t, receipt, decoded(t, http.StatusCreated, replay))
	require.Equal(t, "true", replay.Header().Get("Idempotent-Replayed"))

	refused(t, http.StatusNotFound, "not_found",
		a.sendPublic(http.MethodPost, "/public/contact-enquiries", enquiryBody("no-such-service", "Hello")))
	refused(t, http.StatusBadRequest, "invalid_request",
		a.sendPublic(http.MethodPost, "/public/contact-enquiries", enquiryBody("workshops-programs", strings.Repeat("x", 5001))))

	token := insertSession(t, a.clock.at, "booking_admin")
	list := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/contact-enquiries?status=new&q="+receipt["reference"].(string), token))
	items := list["items"].([]any)
	require.Len(t, items, 1)
	enquiry := items[0].(map[string]any)
	require.Equal(t, "thandar@example.com", enquiry["email"])
	require.Equal(t, "workshops-programs", enquiry["service"].(map[string]any)["slug"])
	id := enquiry["id"].(string)

	require.Equal(t, enquiry, decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/contact-enquiries/"+id, token)))
	handled := decoded(t, http.StatusOK, a.send(http.MethodPost, "/admin/contact-enquiries/"+id+"/mark-handled", token))
	require.Equal(t, "handled", handled["status"])
	require.NotEmpty(t, handled["handledAt"])
	require.Equal(t, handled, decoded(t, http.StatusOK, a.send(http.MethodPost, "/admin/contact-enquiries/"+id+"/mark-handled", token)))

	requireForbidden(t, a.send(http.MethodGet, "/admin/contact-enquiries", insertSession(t, a.clock.at, "content_editor")))
	for _, private := range []string{"Hello there", "thandar@example.com", "Thandar"} {
		require.NotContains(t, a.logs.String(), private)
	}
}
