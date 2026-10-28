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

// postService creates a service through the API and deletes it, with its appointments, after t.
func postService(t *testing.T, a *webtest.API, token, slug string) map[string]any {
	t.Helper()
	body := fmt.Sprintf(`{"slug": %q, "name": {"en": "Couples Art Therapy", "my": "စုံတွဲ"},
		"bookingAction": "request", "durationMinutes": 75, "bufferAfterMinutes": 15}`, slug)
	svc := webtest.JSON(t, http.StatusCreated, a.Admin(http.MethodPost, "/admin/services", token, body))
	t.Cleanup(func() {
		pool := pgtest.Pool(t)
		_, err := pool.Exec(context.Background(), "DELETE FROM appointments WHERE service_id = $1", svc["id"])
		require.NoError(t, err)
		_, err = pool.Exec(context.Background(), "DELETE FROM services WHERE id = $1", svc["id"])
		require.NoError(t, err)
	})
	return svc
}

func TestHTTP_ServiceLifecycle(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	svc := postService(t, a, token, "couples-art-therapy")
	require.Equal(t, []string{"bookingAction", "bufferAfterMinutes", "bufferBeforeMinutes", "createdAt", "durationMinutes",
		"formats", "id", "name", "slug", "sortOrder", "state", "updatedAt", "version"}, keys(svc), "unset texts are omitted")
	require.Equal(t, "active", svc["state"])
	require.Equal(t, []any{"online"}, svc["formats"])
	require.Equal(t, float64(1), svc["version"])
	path := "/admin/services/" + svc["id"].(string)

	list := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/services", token, ""))
	var slugs []string
	for _, item := range list["items"].([]any) {
		slugs = append(slugs, item.(map[string]any)["slug"].(string))
	}
	require.Contains(t, slugs, "couples-art-therapy")
	require.Contains(t, slugs, "free-consultation", "paused services are listed")

	svc = webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPatch, path, token, `{"version": 1, "durationMinutes": 90}`))
	require.Equal(t, float64(90), svc["durationMinutes"])
	require.Equal(t, float64(2), svc["version"])
	require.Equal(t, map[string]any{"en": "Couples Art Therapy", "my": "စုံတွဲ"}, svc["name"], "unpatched fields stay")

	svc = webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/pause", token, `{"version": 2}`))
	require.Equal(t, "paused", svc["state"])
	svc = webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPost, path+"/resume", token, `{"version": 3}`))
	require.Equal(t, "active", svc["state"])
	webtest.Refused(t, http.StatusConflict, "invalid_transition",
		a.Admin(http.MethodPost, path+"/resume", token, `{"version": 4}`))
	webtest.Refused(t, http.StatusConflict, "stale_version", a.Admin(http.MethodPost, path+"/pause", token, `{"version": 1}`))
	require.Equal(t, svc, webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, path, token, "")))

	for _, event := range []string{"service_created", "service_updated", "service_paused", "service_resumed"} {
		require.Contains(t, a.Logs.String(), event)
	}
}

func TestHTTP_ServiceRefusals(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	path := "/admin/services/" + postService(t, a, token, "taken-slug")["id"].(string)

	webtest.Refused(t, http.StatusConflict, "slug_taken", a.Admin(http.MethodPost, "/admin/services", token,
		`{"slug": "taken-slug", "name": {"en": "Again"}, "bookingAction": "enquiry_only"}`))
	p := webtest.Refused(t, http.StatusUnprocessableEntity, "action_not_allowed", a.Admin(http.MethodPost, "/admin/services", token,
		`{"slug": "no-duration", "name": {"en": "No duration"}, "bookingAction": "request"}`))
	require.Equal(t, []any{map[string]any{"field": "/durationMinutes",
		"message": "is required when bookingAction is book or request"}}, p["errors"])
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, "/admin/services", token,
		`{"slug": "french", "name": {"en": "French", "fr": "Français"}, "bookingAction": "enquiry_only"}`))
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, "/admin/services", token,
		`{"slug": "Not A Slug", "bookingAction": "maybe", "formats": []}`))
	require.Equal(t, []any{
		map[string]any{"field": "/slug", "message": "must be lowercase words joined by hyphens"},
		map[string]any{"field": "/name", "message": "is required"},
		map[string]any{"field": "/bookingAction", "message": "must be one of: book, request, enquiry_only, not_bookable"},
		map[string]any{"field": "/formats", "message": "must have at least 1 items"},
	}, p["errors"])

	webtest.Refused(t, http.StatusConflict, "stale_version", a.Admin(http.MethodPatch, path, token, `{"version": 7, "sortOrder": 9}`))
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPatch, path, token, `{"state": "gone"}`))
	require.Equal(t, []any{
		map[string]any{"field": "/version", "message": "is required"},
		map[string]any{"field": "/state", "message": "must be one of: active, paused, archived"},
	}, p["errors"])
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPost, path+"/pause", token, `{}`))
	webtest.Refused(t, http.StatusNotFound, "not_found",
		a.Admin(http.MethodGet, "/admin/services/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10", token, ""))
	webtest.Refused(t, http.StatusNotFound, "not_found",
		a.Admin(http.MethodPost, "/admin/services/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10/resume", token, `{"version": 1}`))
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodGet, "/admin/services/nope", token, ""))
	require.Equal(t, []any{map[string]any{"field": "serviceId", "message": "must be a valid uuid"}}, p["errors"])
}

func TestHTTP_ServicesNeedBookingAdmin(t *testing.T) {
	a := bookingAPI(t)
	editor := webtest.SignIn(t, "content_editor")
	id := "/admin/services/8f14e45f-ceea-4e8a-9b1c-3c1d2a6b7e10"
	for _, call := range [][2]string{
		{http.MethodGet, "/admin/services"}, {http.MethodPost, "/admin/services"}, {http.MethodGet, id},
		{http.MethodPatch, id}, {http.MethodDelete, id}, {http.MethodPost, id + "/pause"}, {http.MethodPost, id + "/resume"},
	} {
		webtest.Refused(t, http.StatusForbidden, "forbidden", a.Admin(call[0], call[1], editor, `{"version": 1}`))
	}
	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, "/admin/services", ""))
	webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/services", webtest.SignIn(t, "site_admin"), ""))
}

func TestHTTP_DeleteService(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")
	path := "/admin/services/" + postService(t, a, token, "never-booked")["id"].(string)
	res := a.Admin(http.MethodDelete, path, token, "")
	require.Equal(t, http.StatusNoContent, res.Code)
	require.Empty(t, res.Body.String())
	webtest.Refused(t, http.StatusNotFound, "not_found", a.Admin(http.MethodDelete, path, token, ""))

	booked, _ := booked(t, booking.Confirmed)
	webtest.Refused(t, http.StatusConflict, "in_use", a.Admin(http.MethodDelete, "/admin/services/"+booked.ServiceID.String(), token, ""))
}
