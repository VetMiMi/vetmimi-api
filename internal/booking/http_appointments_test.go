package booking_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_AdminAppointments(t *testing.T) {
	appt, _ := booked(t, booking.Pending)
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	path := "/admin/appointments/" + appt.ID.String()

	got := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, path, token, ""))
	require.Equal(t, "pending", got["status"])
	require.Equal(t, "visitor@example.com", got["visitorEmail"])
	require.Equal(t, []any{"confirm", "decline", "reschedule", "set_note", "mark_communicated"}, got["allowedActions"])
	require.NotContains(t, got, "visitorPhone", "an empty column is left out, not null")

	body := fmt.Sprintf(`{"version": %d}`, appt.Version)
	confirmed := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/confirm", token, body))
	require.Equal(t, "confirmed", confirmed["status"])
	webtest.Refused(t, http.StatusConflict, "stale_version", a.Admin(http.MethodPost, path+"/cancel", token, body))

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/appointments?view=all&q="+appt.Reference, token, ""))
	require.Equal(t, appt.Reference, list["items"].([]any)[0].(map[string]any)["reference"])
	dashboard := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/dashboard", token, ""))
	require.Equal(t, []string{"attentionRequired", "counts", "pending", "timezone", "today", "upcoming"}, keys(dashboard))

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/appointments?status=pending&status=lost", token, ""))
	require.Equal(t, "status", p["errors"].([]any)[0].(map[string]any)["field"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/appointments/42", token, ""))
	require.Equal(t, "appointmentId", p["errors"].([]any)[0].(map[string]any)["field"])
	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodGet, path, webtest.SignIn(t, "content_editor"), ""))
}

func TestHTTP_CreateManualAppointment(t *testing.T) {
	const key = "3b0f7a52-61c4-4f0c-9d0a-5e8f1b2c3d42"
	practitioner(t)
	clearRules(t)
	_, err := createRule(t, 3, "10:00", "13:00")
	require.NoError(t, err)
	t.Cleanup(func() {
		pool := pgtest.Pool(t)
		_, err := pool.Exec(context.Background(), "DELETE FROM appointments WHERE visitor_email = 'kyaw.phone@example.com'")
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM idempotency_keys WHERE key = $1", key)
		require.NoError(t, err)
	})
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	create := func(body string) map[string]any {
		res := a.Send(http.MethodPost, "/admin/appointments", body, "Authorization", "Bearer "+token, "Idempotency-Key", key)
		got := webtest.JSON(t, http.StatusCreated, res)
		got["replayed"] = res.Header().Get("Idempotent-Replayed")
		return got
	}

	// Wednesday 21 October 2026, 10:00 in Sydney; the clock reads 5 October.
	body := fmt.Sprintf(`{"serviceId": %q, "startsAt": "2026-10-20T23:00:00Z", "format": "online", "locale": "en",
		"visitor": {"name": "Kyaw Phone", "email": "kyaw.phone@example.com"}}`, therapy(t).ID.String())
	first := create(body)
	require.Equal(t, "confirmed", first["status"])
	require.Equal(t, "", first["replayed"])
	require.Equal(t, "true", create(body)["replayed"])

	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Send(http.MethodPost, "/admin/appointments", `{"serviceId": "nope"}`, "Authorization", "Bearer "+token, "Idempotency-Key", key))
	require.Contains(t, p["errors"], map[string]any{"field": "/serviceId", "message": "must be a valid uuid"})
}
