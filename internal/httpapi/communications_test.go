package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func TestCommunicationActions(t *testing.T) {
	const key = "c0a1e5d2-7b3f-4e6a-8d9c-0f1e2a3b4c5d"
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
		_, err = pool.Exec(ctx, "DELETE FROM appointments WHERE visitor_email = 'may.thu@example.com'")
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM idempotency_keys WHERE key = $1", key)
		require.NoError(t, err)
	})
	_, err = booking.CreateRule(ctx, pool, booking.Rule{Weekday: 3, Start: "10:00", End: "13:00"})
	require.NoError(t, err)
	var serviceID string
	require.NoError(t, pool.QueryRow(ctx, "SELECT id::text FROM services WHERE slug = 'individual-art-therapy'").Scan(&serviceID))

	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	body := fmt.Sprintf(`{"serviceId": %q, "startsAt": "2026-10-20T23:00:00Z", "format": "online", "locale": "en",
		"status": "pending", "visitor": {"name": "May Thu", "email": "may.thu@example.com"}}`, serviceID)
	appt := decoded(t, http.StatusCreated, a.createManual(token, key, body))
	path := appointmentsURL + "/" + appt["id"].(string)
	received := appt["communications"].([]any)[0].(map[string]any)["id"].(string)
	resendURL := path + "/communications/" + received + "/resend"

	refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.send(http.MethodPost, resendURL, token))

	_, err = pool.Exec(ctx, "UPDATE communications SET status = 'failed', attempts = 5 WHERE id = $1", received)
	require.NoError(t, err)
	copied := decoded(t, http.StatusCreated, a.send(http.MethodPost, resendURL, token))
	require.Equal(t, "queued", copied["status"])
	require.Equal(t, received, copied["resendOf"])

	manual := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, path+"/mark-communicated", token,
		`{"kind": "request_received", "note": "Phoned her"}`))
	require.Equal(t, "manual", manual["channel"])
	require.Equal(t, "Phoned her", manual["note"])
	refused(t, http.StatusBadRequest, "invalid_request", a.sendJSON(http.MethodPost, path+"/mark-communicated", token,
		`{"kind": "request_received", "note": ""}`))

	detail := decoded(t, http.StatusOK, a.send(http.MethodGet, path, token))
	require.Len(t, detail["communications"], 3, "both writes show on the appointment at once")

	editor := insertSession(t, a.clock.at, "content_editor")
	refused(t, http.StatusForbidden, "forbidden", a.send(http.MethodPost, resendURL, editor))
}
