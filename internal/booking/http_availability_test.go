package booking_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

const rulesURL = "/admin/availability/rules"

func TestHTTP_AvailabilityRules(t *testing.T) {
	clearRules(t)
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")

	afternoon := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "14:00", "endTime": "17:00"}`))
	require.Equal(t, []string{"endTime", "id", "startTime", "weekday"}, keys(afternoon))
	webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "10:00", "endTime": "13:00"}`))

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, rulesURL, token, ""))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	items := list["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "10:00", items[0].(map[string]any)["startTime"])

	webtest.Refused(t, http.StatusConflict, "overlapping_period", a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "12:30", "endTime": "15:00"}`))
	p := webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 3, "startTime": "15:00", "endTime": "09:00"}`))
	require.Equal(t, []any{map[string]any{"field": "/endTime", "message": "must be later than startTime"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 8, "startTime": "9:00", "endTime": "10:00"}`))
	require.Equal(t, []any{
		map[string]any{"field": "/weekday", "message": "must be at most 7"},
		map[string]any{"field": "/startTime", "message": "must be a time as HH:MM"},
	}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, rulesURL, token,
		`{"weekday": 0, "startTime": "09:00", "endTime": "10:00"}`))
	require.Equal(t, []any{map[string]any{"field": "/weekday", "message": "must be at least 1"}}, p["errors"])

	path := rulesURL + "/" + afternoon["id"].(string)
	moved := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPut, path, token,
		`{"weekday": 4, "startTime": "13:00", "endTime": "16:30"}`))
	require.Equal(t, map[string]any{"id": afternoon["id"], "weekday": float64(4), "startTime": "13:00", "endTime": "16:30"}, moved)
	res := a.Admin(http.MethodDelete, path, token, "")
	require.Equal(t, http.StatusNoContent, res.Code)
	require.Empty(t, res.Body.String())
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodDelete, path, token, ""))
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodPut, path, token,
		`{"weekday": 4, "startTime": "13:00", "endTime": "16:30"}`))
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodDelete, rulesURL+"/nope", token, ""))
}

func TestHTTP_ReplaceWeeklyHours(t *testing.T) {
	clearRules(t)
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")

	week := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPut, rulesURL, token,
		`{"periods": [{"weekday": 3, "startTime": "13:00", "endTime": "17:00"}, {"weekday": 1, "startTime": "09:00", "endTime": "12:00"}]}`))
	require.Equal(t, "Australia/Sydney", week["timezone"])
	items := week["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, float64(1), items[0].(map[string]any)["weekday"])
	require.Equal(t, week, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, rulesURL, token, "")))

	webtest.Refused(t, http.StatusConflict, "overlapping_period", a.Admin(http.MethodPut, rulesURL, token,
		`{"periods": [{"weekday": 2, "startTime": "10:00", "endTime": "13:00"}, {"weekday": 2, "startTime": "12:30", "endTime": "15:00"}]}`))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPut, rulesURL, token, `{}`))
	require.Equal(t, []any{map[string]any{"field": "/periods", "message": "is required"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPut, rulesURL, token,
		`{"periods": [{"weekday": 1, "startTime": "09:00"}]}`))
	require.Equal(t, []any{map[string]any{"field": "/periods/0/endTime", "message": "is required"}}, p["errors"])
	require.Len(t, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, rulesURL, token, ""))["items"], 2,
		"a refused week changes nothing")

	empty := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPut, rulesURL, token, `{"periods": []}`))
	require.Equal(t, []any{}, empty["items"])
	require.Equal(t, []any{}, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, rulesURL, token, ""))["items"])
}

func TestHTTP_AvailabilityOverrides(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")

	// 10:00-12:00 on Sunday 7 May 2034 in Sydney (AEST, UTC+10).
	o := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, "/admin/availability/overrides", token,
		`{"onDate": "2034-05-07", "kind": "open", "startsAt": "2034-05-07T00:00:00Z", "endsAt": "2034-05-07T02:00:00Z", "note": "Private extra hours"}`))
	require.Equal(t, []string{"createdAt", "endsAt", "id", "kind", "note", "onDate", "startsAt"}, keys(o))
	require.Equal(t, "2034-05-07", o["onDate"])
	require.Equal(t, "Private extra hours", o["note"])

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/availability/overrides?from=2034-05-07&to=2034-05-07", token, ""))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	require.Equal(t, []any{o}, list["items"])
	empty := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/availability/overrides?from=2034-05-08", token, ""))
	require.Equal(t, []any{}, empty["items"])

	path := "/admin/availability/overrides/" + o["id"].(string)
	replaced := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPut, path, token,
		`{"onDate": "2034-05-07", "kind": "replace", "startsAt": "2034-05-07T01:00:00Z", "endsAt": "2034-05-07T02:00:00Z"}`))
	require.Equal(t, "replace", replaced["kind"])
	require.NotContains(t, replaced, "note", "a note left out is cleared")

	p := webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.Admin(http.MethodPost, "/admin/availability/overrides", token,
		`{"onDate": "2034-05-07", "kind": "open", "startsAt": "2034-05-07T13:00:00Z", "endsAt": "2034-05-07T15:00:00Z"}`))
	require.Equal(t, "/endsAt", p["errors"].([]any)[0].(map[string]any)["field"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, "/admin/availability/overrides", token,
		`{"onDate": "7 May", "kind": "close", "startsAt": "2034-05-07T00:00:00Z", "endsAt": "2034-05-07T02:00:00Z"}`))
	require.Equal(t, []any{
		map[string]any{"field": "/onDate", "message": "must be a valid date"},
		map[string]any{"field": "/kind", "message": "must be one of: open, replace"},
	}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/availability/overrides?from=2034-05-08&to=2034-05-07", token, ""))
	require.Equal(t, []any{map[string]any{"field": "to", "message": "must not be before from"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/availability/overrides?from=7-May", token, ""))
	require.Equal(t, []any{map[string]any{"field": "from", "message": "must be a valid date"}}, p["errors"])

	res := a.Admin(http.MethodDelete, path, token, "")
	require.Equal(t, http.StatusNoContent, res.Code)
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodDelete, path, token, ""))
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodPut, path, token,
		`{"onDate": "2034-05-07", "kind": "replace", "startsAt": "2034-05-07T00:00:00Z", "endsAt": "2034-05-07T02:00:00Z"}`))
	require.NotContains(t, a.Logs.String(), "Private extra hours")
}

func TestHTTP_AvailabilityBlockListsConflicts(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	appt, _ := booked(t, booking.Pending)
	day := appt.StartsAt.Format(time.DateOnly)
	body := `{"startsAt": "` + appt.StartsAt.Add(-time.Hour).UTC().Format(time.RFC3339) + `", "endsAt": "` +
		appt.StartsAt.Add(2*time.Hour).UTC().Format(time.RFC3339) + `", "allDay": true, "reason": "Private family matter"}`

	saved := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, "/admin/availability/blocks", token, body))
	block := saved["block"].(map[string]any)
	require.Equal(t, []string{"allDay", "createdAt", "endsAt", "id", "reason", "startsAt"}, keys(block))
	require.Equal(t, "Private family matter", block["reason"], "admins see the private reason")
	conflicts := saved["conflicts"].([]any)
	require.Len(t, conflicts, 1)
	c := conflicts[0].(map[string]any)
	require.Equal(t, []string{"createdAt", "durationMinutes", "endsAt", "format", "holdExpiresAt", "id", "reference",
		"service", "source", "startsAt", "status", "timezone", "updatedAt", "visitorName"}, keys(c), "no visitor contact details")
	require.Equal(t, appt.Reference, c["reference"])
	require.Equal(t, "pending", c["status"])
	require.Equal(t, "individual-art-therapy", c["service"].(map[string]any)["slug"])

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/availability/blocks?from="+day+"&to="+day, token, ""))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	require.Equal(t, []any{block}, list["items"])

	path := "/admin/availability/blocks/" + block["id"].(string)
	moved := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPut, path, token,
		`{"startsAt": "2034-03-02T00:00:00Z", "endsAt": "2034-03-02T02:00:00Z"}`))
	require.Equal(t, []any{}, moved["conflicts"])
	require.Equal(t, false, moved["block"].(map[string]any)["allDay"])
	webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.Admin(http.MethodPut, path, token,
		`{"startsAt": "2034-03-02T02:00:00Z", "endsAt": "2034-03-02T02:00:00Z"}`))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, "/admin/availability/blocks", token,
		`{"startsAt": "tomorrow"}`))
	require.Equal(t, []any{
		map[string]any{"field": "/startsAt", "message": "must be a valid date-time"},
		map[string]any{"field": "/endsAt", "message": "is required"},
	}, p["errors"])

	require.Equal(t, http.StatusNoContent, a.Admin(http.MethodDelete, path, token, "").Code)
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodDelete, path, token, ""))
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodPut, path, token,
		`{"startsAt": "2034-03-02T00:00:00Z", "endsAt": "2034-03-02T02:00:00Z"}`))
	require.NotContains(t, a.Logs.String(), "Private family matter")
	require.NotContains(t, a.Logs.String(), "visitor@example.com")
}

func TestHTTP_PreviewAvailability(t *testing.T) {
	clearRules(t)
	_, err := createRule(t, tuesday, "10:00", "13:00")
	require.NoError(t, err)
	setBookingEnabled(t, false)
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	id := therapy(t).ID.String()

	got := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet,
		"/admin/availability/preview?serviceId="+strings.ToUpper(id)+"&from=2026-10-01&to=2026-10-14", token, ""))
	require.Equal(t, []string{"days", "from", "serviceId", "timezone", "to"}, keys(got))
	require.Equal(t, id, got["serviceId"])
	require.Equal(t, "2026-10-05", got["from"], "from is moved to today")
	day := got["days"].([]any)[0].(map[string]any)
	require.Equal(t, "2026-10-13", day["date"], "shown while public booking is paused")
	require.Equal(t, map[string]any{"startsAt": "2026-10-12T23:00:00Z", "endsAt": "2026-10-13T00:00:00Z"},
		day["slots"].([]any)[0])

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/availability/preview?serviceId="+id+"&from=7-May", token, ""))
	require.Equal(t, []any{
		map[string]any{"field": "from", "message": "must be a valid date"},
		map[string]any{"field": "to", "message": "is required"},
	}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/availability/preview?serviceId=nope&from=2026-10-01&to=2026-10-02", token, ""))
	require.Equal(t, []any{map[string]any{"field": "serviceId", "message": "must be a valid uuid"}}, p["errors"])
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodGet,
		"/admin/availability/preview?serviceId=8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10&from=2026-10-06&to=2026-10-07", token, ""))
}

func TestHTTP_AvailabilityNeedsBookingAdmin(t *testing.T) {
	a := bookingAPI(t)
	editor := webtest.SignIn(t, "content_editor")
	id := "8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10"
	for _, call := range [][2]string{
		{http.MethodGet, rulesURL}, {http.MethodPost, rulesURL}, {http.MethodPut, rulesURL},
		{http.MethodPut, rulesURL + "/" + id}, {http.MethodDelete, rulesURL + "/" + id},
		{http.MethodGet, "/admin/availability/overrides"}, {http.MethodPost, "/admin/availability/overrides"},
		{http.MethodPut, "/admin/availability/overrides/" + id}, {http.MethodDelete, "/admin/availability/overrides/" + id},
		{http.MethodGet, "/admin/availability/blocks"}, {http.MethodPost, "/admin/availability/blocks"},
		{http.MethodPut, "/admin/availability/blocks/" + id}, {http.MethodDelete, "/admin/availability/blocks/" + id},
		{http.MethodGet, "/admin/availability/preview"},
	} {
		webtest.Refused(t, http.StatusForbidden, "forbidden", a.Admin(call[0], call[1], editor, `{"periods": []}`))
	}
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, rulesURL, ""))
}
