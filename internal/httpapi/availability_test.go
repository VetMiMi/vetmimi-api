package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

const rulesURL = "/admin/availability/rules"

func TestAvailabilityRules(t *testing.T) {
	ctx := context.Background()
	_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM availability_rules")
	require.NoError(t, err)
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")

	afternoon := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "14:00", "endTime": "17:00"}`))
	require.Equal(t, "14:00", afternoon["startTime"])
	decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "10:00", "endTime": "13:00"}`))

	list := decoded(t, http.StatusOK, a.send(http.MethodGet, rulesURL, token))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	items := list["items"].([]any)
	require.Len(t, items, 2)
	require.Equal(t, "10:00", items[0].(map[string]any)["startTime"])

	refused(t, http.StatusConflict, "overlapping_period", a.sendJSON(http.MethodPost, rulesURL, token,
		`{"weekday": 2, "startTime": "12:30", "endTime": "15:00"}`))
	p := refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPost, rulesURL, token,
		`{"weekday": 3, "startTime": "15:00", "endTime": "09:00"}`))
	require.Equal(t, []any{map[string]any{"field": "/endTime", "message": "must be later than startTime"}}, p["errors"])
	refused(t, http.StatusBadRequest, "invalid_request", a.sendJSON(http.MethodPost, rulesURL, token,
		`{"weekday": 8, "startTime": "09:00", "endTime": "10:00"}`))

	path := rulesURL + "/" + afternoon["id"].(string)
	moved := decoded(t, http.StatusOK, a.sendJSON(http.MethodPut, path, token,
		`{"weekday": 4, "startTime": "13:00", "endTime": "16:30"}`))
	require.Equal(t, map[string]any{"id": afternoon["id"], "weekday": float64(4), "startTime": "13:00", "endTime": "16:30"}, moved)
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, path, token).Code)
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodDelete, path, token))

	requireForbidden(t, a.send(http.MethodGet, rulesURL, insertSession(t, a.clock.at, "content_editor")))
	_, err = pgtest.Pool(t).Exec(ctx, "DELETE FROM availability_rules")
	require.NoError(t, err)
}

func TestAvailabilityOverrides(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")

	// 10:00-12:00 on Sunday 7 September 2031 in Sydney (AEST, UTC+10).
	o := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, "/admin/availability/overrides", token,
		`{"onDate": "2031-09-07", "kind": "open", "startsAt": "2031-09-07T00:00:00Z", "endsAt": "2031-09-07T02:00:00Z", "note": "Extra"}`))
	require.Equal(t, "2031-09-07", o["onDate"])
	require.Equal(t, "Extra", o["note"])

	list := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/availability/overrides?from=2031-09-07&to=2031-09-07", token))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	require.Equal(t, []any{o}, list["items"])

	p := refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPost, "/admin/availability/overrides", token,
		`{"onDate": "2031-09-07", "kind": "open", "startsAt": "2031-09-07T13:00:00Z", "endsAt": "2031-09-07T15:00:00Z"}`))
	require.Equal(t, "/endsAt", p["errors"].([]any)[0].(map[string]any)["field"])

	path := "/admin/availability/overrides/" + o["id"].(string)
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, path, token).Code)
	refused(t, http.StatusNotFound, "not_found", a.sendJSON(http.MethodPut, path, token,
		`{"onDate": "2031-09-07", "kind": "replace", "startsAt": "2031-09-07T00:00:00Z", "endsAt": "2031-09-07T02:00:00Z"}`))
	refused(t, http.StatusBadRequest, "invalid_request",
		a.send(http.MethodGet, "/admin/availability/overrides?from=2031-09-08&to=2031-09-07", token))
}

func TestAvailabilityBlockListsConflicts(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	svc := a.createService(t, token, "block-conflicts")
	appt := bookOnce(t, svc["id"].(string), time.Date(2033, 3, 1, 0, 0, 0, 0, time.UTC), booking.Pending)

	saved := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, "/admin/availability/blocks", token,
		`{"startsAt": "2033-02-28T13:00:00Z", "endsAt": "2033-03-01T13:00:00Z", "allDay": true, "reason": "Away"}`))
	block := saved["block"].(map[string]any)
	require.Equal(t, true, block["allDay"])
	require.Equal(t, "Away", block["reason"], "admins see the private reason")
	conflicts := saved["conflicts"].([]any)
	require.Len(t, conflicts, 1)
	c := conflicts[0].(map[string]any)
	require.Equal(t, appt.Reference, c["reference"])
	require.Equal(t, "pending", c["status"])
	require.Equal(t, "block-conflicts", c["service"].(map[string]any)["slug"])
	require.NotContains(t, c, "visitorEmail")

	list := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/availability/blocks?from=2033-03-01&to=2033-03-01", token))
	require.Equal(t, []any{block}, list["items"])

	path := "/admin/availability/blocks/" + block["id"].(string)
	moved := decoded(t, http.StatusOK, a.sendJSON(http.MethodPut, path, token,
		`{"startsAt": "2033-03-02T00:00:00Z", "endsAt": "2033-03-02T02:00:00Z"}`))
	require.Empty(t, moved["conflicts"])
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPut, path, token,
		`{"startsAt": "2033-03-02T02:00:00Z", "endsAt": "2033-03-02T02:00:00Z"}`))
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, path, token).Code)
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodDelete, path, token))

	requireForbidden(t, a.send(http.MethodGet, "/admin/availability/blocks", insertSession(t, a.clock.at, "content_editor")))
}
