package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// sendJSON sends body as JSON with the session token.
func (a *authAPI) sendJSON(method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

// decoded asserts res has status and returns its JSON body.
func decoded(t *testing.T, status int, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	require.Equal(t, status, res.Code, res.Body.String())
	var body map[string]any
	require.NoError(t, json.NewDecoder(bytes.NewReader(res.Body.Bytes())).Decode(&body))
	return body
}

// refused asserts res is a problem with status and code.
func refused(t *testing.T, status int, code string, res *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	p := decoded(t, status, res)
	require.Equal(t, code, p["code"])
	return p
}

// deleteServiceRow removes a service a test made, and its appointments, so
// the seed tests see only the four launch services.
func deleteServiceRow(t *testing.T, id string) {
	t.Helper()
	t.Cleanup(func() {
		pool := pgtest.Pool(t)
		_, err := pool.Exec(context.Background(), "DELETE FROM appointments WHERE service_id = $1", id)
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM services WHERE id = $1", id)
		require.NoError(t, err)
	})
}

func (a *authAPI) createService(t *testing.T, token, slug string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"slug": %q, "name": {"en": "Couples Art Therapy", "my": "စုံတွဲ"},
		"bookingAction": "request", "durationMinutes": 75, "bufferAfterMinutes": 15}`, slug)
	svc := decoded(t, http.StatusCreated, a.sendJSON(http.MethodPost, "/admin/services", token, body))
	deleteServiceRow(t, svc["id"].(string))
	return svc
}

func TestServiceLifecycle(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	svc := a.createService(t, token, "couples-art-therapy")
	require.Equal(t, "active", svc["state"])
	require.Equal(t, []any{"online"}, svc["formats"])
	require.Equal(t, float64(0), svc["bufferBeforeMinutes"])
	require.Equal(t, float64(1), svc["version"])
	path := "/admin/services/" + svc["id"].(string)

	list := decoded(t, http.StatusOK, a.send(http.MethodGet, "/admin/services", token))
	var slugs []string
	for _, item := range list["items"].([]any) {
		slugs = append(slugs, item.(map[string]any)["slug"].(string))
	}
	require.Contains(t, slugs, "couples-art-therapy")
	require.Contains(t, slugs, "free-consultation", "paused services are listed")

	svc = decoded(t, http.StatusOK, a.sendJSON(http.MethodPatch, path, token, `{"version": 1, "durationMinutes": 90}`))
	require.Equal(t, float64(90), svc["durationMinutes"])
	require.Equal(t, float64(2), svc["version"])
	require.Equal(t, map[string]any{"en": "Couples Art Therapy", "my": "စုံတွဲ"}, svc["name"], "unpatched fields stay")

	svc = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/pause", token, `{"version": 2}`))
	require.Equal(t, "paused", svc["state"])
	svc = decoded(t, http.StatusOK, a.sendJSON(http.MethodPost, path+"/resume", token, `{"version": 3}`))
	require.Equal(t, "active", svc["state"])
	svc = decoded(t, http.StatusOK, a.sendJSON(http.MethodPatch, path, token, `{"version": 4, "state": "archived"}`))
	require.Equal(t, "archived", svc["state"])

	refused(t, http.StatusConflict, "invalid_transition", a.sendJSON(http.MethodPost, path+"/resume", token, `{"version": 5}`))
	svc = decoded(t, http.StatusOK, a.sendJSON(http.MethodPatch, path, token, `{"version": 5, "state": "paused"}`))
	require.Equal(t, "paused", svc["state"], "an archived service comes back paused")
	require.Equal(t, svc, decoded(t, http.StatusOK, a.send(http.MethodGet, path, token)))
}

func TestServiceRefusals(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	svc := a.createService(t, token, "taken-slug")
	path := "/admin/services/" + svc["id"].(string)

	refused(t, http.StatusConflict, "slug_taken", a.sendJSON(http.MethodPost, "/admin/services", token,
		`{"slug": "taken-slug", "name": {"en": "Again"}, "bookingAction": "enquiry_only"}`))
	p := refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPost, "/admin/services", token,
		`{"slug": "no-duration", "name": {"en": "No duration"}, "bookingAction": "request"}`))
	require.Equal(t, []any{map[string]any{"field": "/durationMinutes",
		"message": "is required when bookingAction is book or request"}}, p["errors"])
	refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.sendJSON(http.MethodPost, "/admin/services", token,
		`{"slug": "no-english", "name": {"my": "မြန်မာ"}, "bookingAction": "enquiry_only"}`))
	refused(t, http.StatusConflict, "stale_version", a.sendJSON(http.MethodPatch, path, token, `{"version": 7, "sortOrder": 9}`))
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodGet, "/admin/services/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10", token))

	editor := insertSession(t, a.clock.at, "content_editor")
	requireForbidden(t, a.send(http.MethodGet, "/admin/services", editor))
	requireForbidden(t, a.sendJSON(http.MethodPost, path+"/pause", editor, `{"version": 1}`))
}

func TestDeleteService(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	unused := a.createService(t, token, "never-booked")
	path := "/admin/services/" + unused["id"].(string)
	require.Equal(t, http.StatusNoContent, a.send(http.MethodDelete, path, token).Code)
	refused(t, http.StatusNotFound, "not_found", a.send(http.MethodGet, path, token))

	booked := a.createService(t, token, "booked-once")
	bookOnce(t, booked["id"].(string), time.Date(2033, 2, 1, 9, 0, 0, 0, time.UTC), booking.Confirmed)
	refused(t, http.StatusConflict, "in_use", a.send(http.MethodDelete, "/admin/services/"+booked["id"].(string), token))
}

// bookOnce inserts an appointment for the service, as the booking flow will.
func bookOnce(t *testing.T, serviceID string, startsAt time.Time, status booking.Status) db.Appointment {
	t.Helper()
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	var id pgtype.UUID
	require.NoError(t, id.Scan(serviceID))
	svc, err := q.GetService(ctx, id)
	require.NoError(t, err)
	userID, _ := createUser(t, []byte("sealed"), "site_admin")
	appt, err := booking.InsertAppointment(ctx, q, []byte("test signing secret, 32 bytes ok"), booking.NewAppointment{
		PractitionerID: userID, Service: svc, StartsAt: startsAt, Duration: time.Hour, Status: status,
		Timezone: "Australia/Sydney", Format: "online", Locale: "en", Source: "manual",
		VisitorName: "Visitor", VisitorEmail: "visitor@example.com",
		HoldExpiresAt: sql.NullTime{Time: startsAt, Valid: status == booking.Pending},
	})
	require.NoError(t, err)
	return appt
}
