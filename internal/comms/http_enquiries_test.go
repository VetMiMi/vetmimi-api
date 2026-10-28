package comms_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

const enquiryBody = `{"name": "Thandar", "email": "Thandar@Example.com", "enquiryType": "workshop",
	"service": "workshops-programs", "subject": "Team workshop", "message": "Hello there", "locale": "en",
	"privacyAcknowledged": true}`

func enquiryAPI(t *testing.T) *webtest.API {
	h := &comms.Handler{Pool: pgtest.Pool(t), Now: webtest.Clock}
	return webtest.New(t, h.Routes)
}

func TestHTTP_ContactEnquiry(t *testing.T) {
	a := enquiryAPI(t)
	const key = "8a0e7c1d-2b3f-4e5a-9c6d-7e8f9a0b1c2d"
	receipt := webtest.JSON(t, http.StatusCreated, a.Public(http.MethodPost, "/public/contact-enquiries", enquiryBody,
		"Idempotency-Key", key))
	require.Len(t, receipt, 2, "reference and createdAt only")
	require.Regexp(t, `^EN-`, receipt["reference"])
	replay := a.Public(http.MethodPost, "/public/contact-enquiries", enquiryBody, "Idempotency-Key", key)
	require.Equal(t, receipt, webtest.JSON(t, http.StatusCreated, replay))
	require.Equal(t, "true", replay.Header().Get("Idempotent-Replayed"))

	token := webtest.SignIn(t, "booking_admin")
	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet,
		"/admin/contact-enquiries?status=new&q="+receipt["reference"].(string), token, ""))
	items := list["items"].([]any)
	require.Len(t, items, 1)
	enquiry := items[0].(map[string]any)
	require.Equal(t, "thandar@example.com", enquiry["email"])
	require.Equal(t, "workshops-programs", enquiry["service"].(map[string]any)["slug"])
	require.NotContains(t, enquiry, "handledAt")
	id := enquiry["id"].(string)

	require.Equal(t, enquiry, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/contact-enquiries/"+id, token, "")))
	handled := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, "/admin/contact-enquiries/"+id+"/mark-handled", token, ""))
	require.Equal(t, "handled", handled["status"])
	require.NotEmpty(t, handled["handledAt"])

	empty := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/contact-enquiries?q=no-such-enquiry", token, ""))
	require.Equal(t, []any{}, empty["items"])

	for _, private := range []string{"Hello there", "thandar@example.com", "Thandar"} {
		require.NotContains(t, a.Logs.String(), private)
	}
}

func TestHTTP_ContactEnquiryRefusals(t *testing.T) {
	a := enquiryAPI(t)
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Public(http.MethodPost, "/public/contact-enquiries",
		strings.Replace(enquiryBody, "Thandar@Example.com", "nope", 1)))
	require.Equal(t, []any{map[string]any{"field": "/email", "message": "must be a valid email"}}, p["errors"])

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodGet, "/admin/contact-enquiries", webtest.SignIn(t, "content_editor"), ""))
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/contact-enquiries/not-a-uuid", webtest.SignIn(t, "booking_admin"), ""))
	require.Equal(t, "enquiryId", p["errors"].([]any)[0].(map[string]any)["field"])
}
