package booking_test

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/web"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func bookingAPI(t *testing.T) *webtest.API {
	h := &booking.Handler{Pool: pgtest.Pool(t), Secret: testSecret, Now: webtest.Clock}
	return webtest.New(t, func(r web.Routes) { h.Routes(r) })
}

func keys(v any) []string {
	return slices.Sorted(maps.Keys(v.(map[string]any)))
}

func TestHTTP_PublicServices(t *testing.T) {
	a := bookingAPI(t)
	list := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/booking/services?locale=my", ""))
	require.Equal(t, true, list["bookingEnabled"])
	require.Equal(t, "Australia/Sydney", list["timezone"])
	require.Equal(t, map[string]any{
		"slug": "individual-art-therapy", "name": "တစ်ဦးချင်း အနုပညာကုထုံး", "bookingAction": "request",
		"durationMinutes": float64(60), "formats": []any{"online"},
	}, list["items"].([]any)[0], "no id, state, buffers or other admin fields")

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Public(http.MethodGet, "/public/booking/services?locale=fr", ""))
	require.Equal(t, []any{map[string]any{"field": "locale", "message": "must be one of: en, my"}}, p["errors"])
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated",
		a.Send(http.MethodGet, "/public/booking/services", "", "X-Service-Key", "wrong"))

	setBookingEnabled(t, false)
	list = webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/booking/services", ""))
	require.Equal(t, false, list["bookingEnabled"])
	require.Equal(t, []any{}, list["items"])
}

func TestHTTP_PublicAvailability(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	a := bookingAPI(t)
	path := "/public/availability?service=individual-art-therapy&from=2026-10-01&to=2026-10-14"

	// The clock reads Monday 5 October, 20:30 in Sydney: the notice period empties the 6th.
	got := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, path, ""))
	require.Equal(t, []string{"days", "from", "service", "timezone", "to"}, keys(got))
	require.Equal(t, "2026-10-05", got["from"], "from is moved to today")
	day := got["days"].([]any)[0].(map[string]any)
	require.Equal(t, "2026-10-13", day["date"])
	require.Equal(t, map[string]any{"startsAt": "2026-10-12T23:00:00Z", "endsAt": "2026-10-13T00:00:00Z"},
		day["slots"].([]any)[0])

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Public(http.MethodGet, "/public/availability?service=individual-art-therapy&to=2026-10-14", ""))
	require.Equal(t, "from", p["errors"].([]any)[0].(map[string]any)["field"])
	webtest.Refused(t, http.StatusNotFound, "not_found",
		a.Public(http.MethodGet, "/public/availability?service=no-such-service&from=2026-10-06&to=2026-10-07", ""))

	setBookingEnabled(t, false)
	webtest.Refused(t, http.StatusUnprocessableEntity, "booking_paused", a.Public(http.MethodGet, path, ""))
}

const visitorBody = `{"service": "individual-art-therapy", "startsAt": "2026-10-19T23:00:00Z", "format": "online",
	"locale": "en", "privacyAcknowledged": true, "policyAcknowledged": true,
	"visitor": {"name": "Thandar Visitor", "email": "thandar@example.com", "note": "Quiet room please"}}`

func TestHTTP_RequestAppointment(t *testing.T) {
	practitioner(t)
	clearRules(t)
	_, err := createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(context.Background(), "DELETE FROM appointments WHERE visitor_email = 'thandar@example.com'")
		require.NoError(t, err)
	})
	a := bookingAPI(t)
	request := func(key, body string) *httptest.ResponseRecorder {
		return a.Public(http.MethodPost, "/public/appointments", body, "Idempotency-Key", key)
	}

	const key = "6f1c2c1e-4d7a-4a51-9f0e-0c6f2d9f8b11"
	res := request(key, visitorBody)
	receipt := webtest.JSON(t, http.StatusCreated, res)
	require.Empty(t, res.Header().Get("Idempotent-Replayed"))
	require.Equal(t, []string{"durationMinutes", "endsAt", "format", "reference", "service", "startsAt", "status",
		"timezone"}, keys(receipt), "no id or management token")
	require.Equal(t, "pending", receipt["status"])

	replay := request(key, visitorBody)
	require.Equal(t, receipt, webtest.JSON(t, http.StatusCreated, replay))
	require.Equal(t, "true", replay.Header().Get("Idempotent-Replayed"))
	webtest.Refused(t, http.StatusUnprocessableEntity, "idempotency_key_reused",
		request(key, strings.Replace(visitorBody, "23:00", "23:30", 1)))

	webtest.Refused(t, http.StatusBadRequest, "invalid_request", request("not-a-uuid", visitorBody))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		request("0b6f6a3e-5c43-4d0e-8a43-2f3b8c1e7d20", strings.Replace(visitorBody, "thandar@example.com", "nope", 1)))
	require.Equal(t, []any{map[string]any{"field": "/visitor/email", "message": "must be a valid email"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		request("0b6f6a3e-5c43-4d0e-8a43-2f3b8c1e7d20", strings.Replace(visitorBody, `"locale"`, `"lang"`, 1)))
	require.Equal(t, "/lang", p["errors"].([]any)[0].(map[string]any)["field"], "an unknown field is refused")

	for _, private := range []string{"Thandar", "thandar@example.com", "Quiet room"} {
		require.NotContains(t, a.Logs.String(), private)
	}
}

func TestHTTP_ManageLink(t *testing.T) {
	appt, _ := booked(t, booking.Confirmed)
	token := linkOf(appt)
	a := bookingAPI(t)

	got := webtest.JSON(t, http.StatusOK, a.Public(http.MethodGet, "/public/manage/"+token, ""))
	require.Equal(t, []string{"canCancel", "canRequestReschedule", "cancellationNoticeHours", "durationMinutes",
		"endsAt", "format", "lateIfCancelledNow", "reference", "rescheduleRequested", "service", "startsAt",
		"status", "timezone"}, keys(got), "no visitor details or notes")
	require.Equal(t, "confirmed", got["status"])

	requested := webtest.JSON(t, http.StatusAccepted, a.Public(http.MethodPost, "/public/manage/"+token+"/reschedule-request",
		`{"preferredTimes": ["2031-02-17T23:00:00Z"], "message": "Mornings please"}`))
	require.Equal(t, true, requested["rescheduleRequested"])
	require.Equal(t, "confirmed", requested["status"], "the time stays booked")

	cancelled := webtest.JSON(t, http.StatusOK, a.Public(http.MethodPost, "/public/manage/"+token+"/cancel", ""))
	require.Equal(t, "cancelled_by_client", cancelled["status"])
	webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed",
		a.Public(http.MethodPost, "/public/manage/"+token+"/cancel", `{"message": "Sorry"}`))

	webtest.Refused(t, http.StatusNotFound, "not_found", a.Public(http.MethodGet, "/public/manage/"+strings.Repeat("Z", 43), ""))
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, "/public/manage/"+token, ""))
	for _, private := range []string{token, "Mornings please"} {
		require.NotContains(t, a.Logs.String(), private)
	}
}
