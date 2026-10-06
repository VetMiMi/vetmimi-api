package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

const appointmentsURL = "/admin/appointments"

func (a *authAPI) createManual(token, key, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, appointmentsURL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

func kinds(t *testing.T, detail map[string]any, list string) []string {
	t.Helper()
	var out []string
	for _, item := range detail[list].([]any) {
		out = append(out, item.(map[string]any)["kind"].(string))
	}
	return out
}

func TestAdminAppointmentLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := pgtest.Pool(t)
	_, err := pool.Exec(ctx, `INSERT INTO users (email, display_name, password_hash, roles, is_practitioner, totp_secret_enc)
		VALUES ('mi@example.com', 'Daw Mi', 'x', '{site_admin}', true, 'sealed') ON CONFLICT DO NOTHING`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "DELETE FROM availability_rules")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "DELETE FROM availability_rules")
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM appointments WHERE visitor_email = 'kyaw.phone@example.com'")
		require.NoError(t, err)
	})
	_, err = booking.CreateRule(ctx, pool, booking.Rule{Weekday: 3, Start: "10:00", End: "13:00"})
	require.NoError(t, err)
	var serviceID string
	require.NoError(t, pool.QueryRow(ctx, "SELECT id::text FROM services WHERE slug = 'individual-art-therapy'").Scan(&serviceID))

	// Wednesday 21 October 2026, 10:00 in Sydney; the clock reads 5 October.
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	const key = "3b0f7a52-61c4-4f0c-9d0a-5e8f1b2c3d41"
	body := fmt.Sprintf(`{"serviceId": %q, "startsAt": "2026-10-20T23:00:00Z", "format": "online", "locale": "en",
		"status": "pending", "visitor": {"name": "Kyaw Phone", "email": "kyaw.phone@example.com", "note": "Side door"}}`,
		serviceID)
	appt := decoded(t, http.StatusCreated, a.createManual(token, key, body))
	require.Equal(t, "pending", appt["status"])
	require.Equal(t, "manual", appt["source"])
	require.Equal(t, float64(1), appt["version"])
	require.Equal(t, []string{"created"}, kinds(t, appt, "events"))
	require.Equal(t, []string{"request_received"}, kinds(t, appt, "communications"))
	require.Equal(t, []any{"confirm", "decline", "reschedule", "set_note", "mark_communicated"}, appt["allowedActions"])
	path := appointmentsURL + "/" + appt["id"].(string)

	replay := a.createManual(token, key, body)
	require.Equal(t, appt["id"], decoded(t, http.StatusCreated, replay)["id"])
	require.Equal(t, "true", replay.Header().Get("Idempotent-Replayed"))

	list := decoded(t, http.StatusOK, a.send(http.MethodGet, appointmentsURL+"?q=kyaw.phone", token))
	require.Equal(t, "Australia/Sydney", list["timezone"])
	items := list["items"].([]any)
	require.Len(t, items, 1)
	summary := items[0].(map[string]any)
	require.Equal(t, "Kyaw Phone", summary["visitorName"])
	for _, private := range []string{"visitorEmail", "visitorPhone", "visitorNote", "adminNote"} {
		require.NotContains(t, summary, private, "the list shows the name only")
	}
	require.Equal(t, appt, decoded(t, http.StatusOK, a.send(http.MethodGet, path, token)))

	appt = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/confirm", token, `{"version": 1}`))
	require.Equal(t, "confirmed", appt["status"])
	require.NotContains(t, appt, "holdExpiresAt")
	require.Equal(t, []string{"request_received", "booking_confirmed", "reminder"}, kinds(t, appt, "communications"))
	refused(t, http.StatusConflict, "stale_version", a.sendJSON(http.MethodPost, path+"/confirm", token, `{"version": 1}`))
	refused(t, http.StatusConflict, "invalid_transition", a.sendJSON(http.MethodPost, path+"/decline", token, `{"version": 2}`))
	refused(t, http.StatusConflict, "slot_unavailable", a.sendJSON(http.MethodPost, path+"/reschedule", token,
		`{"version": 2, "startsAt": "2026-10-21T02:30:00Z"}`))

	appt = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/reschedule", token,
		`{"version": 2, "startsAt": "2026-10-21T00:00:00Z"}`))
	require.Equal(t, "2026-10-21T00:00:00Z", appt["startsAt"])
	events := appt["events"].([]any)
	moved := events[len(events)-1].(map[string]any)
	require.Equal(t, "rescheduled", moved["kind"])
	require.Equal(t, "2026-10-20T23:00:00Z", moved["previousStartsAt"])

	appt = decoded(t, http.StatusOK, a.sendJSON(http.MethodPut, path+"/note", token, `{"version": 3, "note": "Call first"}`))
	require.Equal(t, "Call first", appt["adminNote"])
	appt = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/cancel", token,
		`{"version": 4, "messageToVisitor": "Sorry, I am unwell"}`))
	require.Equal(t, "cancelled_by_practitioner", appt["status"])
	refused(t, http.StatusConflict, "invalid_transition", a.sendJSON(http.MethodPost, path+"/complete", token, `{"version": 5}`))

	for _, private := range []string{"Kyaw", "kyaw.phone@example.com", "Side door", "Call first", "unwell"} {
		require.NotContains(t, a.logs.String(), private)
	}
}

func TestAdminAppointmentRefusals(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodGet, appointmentsURL+"/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10", token))
	refused(t, http.StatusBadRequest, "invalid_request", a.send(http.MethodGet, appointmentsURL+"?limit=101", token))
	refused(t, http.StatusBadRequest, "invalid_request", a.send(http.MethodGet, appointmentsURL+"?cursor=tampered", token))
	editor := insertSession(t, a.clock.at, "content_editor")
	requireForbidden(t, a.send(http.MethodGet, appointmentsURL, editor))
	requireForbidden(t, a.sendJSON(http.MethodPost, appointmentsURL+"/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10/confirm",
		editor, `{"version": 1}`))
}
