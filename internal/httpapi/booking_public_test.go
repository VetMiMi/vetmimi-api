package httpapi

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// public sends a GET as the site does, with the service key and the
// visitor's address.
func (a *authAPI) public(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Service-Key", testServiceKey)
	req.Header.Set("X-Visitor-IP", "198.51.100.200")
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

// pauseBooking turns public booking off until the test ends.
func pauseBooking(t *testing.T) {
	t.Helper()
	set := func(v bool) {
		_, err := pgtest.Pool(t).Exec(context.Background(),
			"UPDATE settings SET value = to_jsonb($1::boolean) WHERE key = 'public_booking_enabled'", v)
		require.NoError(t, err)
	}
	set(false)
	t.Cleanup(func() { set(true) })
}

func keys(v any) []string {
	return slices.Sorted(maps.Keys(v.(map[string]any)))
}

func TestPublicBookableServices(t *testing.T) {
	a := newAuthAPI(t)
	list := decoded(t, http.StatusOK, a.public("/public/booking/services?locale=my"))
	require.Equal(t, true, list["bookingEnabled"])
	require.Equal(t, "request_approval", list["bookingMode"])
	require.Equal(t, "Australia/Sydney", list["timezone"])
	items := list["items"].([]any)
	require.Len(t, items, 1, "only the seeded active service that can be requested")
	require.Equal(t, map[string]any{
		"slug": "individual-art-therapy", "name": "တစ်ဦးချင်း အနုပညာကုထုံး", "bookingAction": "request",
		"durationMinutes": float64(60), "formats": []any{"online"},
	}, items[0], "no id, state, buffers or other admin fields")

	refused(t, http.StatusBadRequest, "invalid_request", a.public("/public/booking/services?locale=fr"))
	requireUnauthenticated(t, a.send(http.MethodGet, "/public/booking/services", "admin-token-is-not-a-key"))

	pauseBooking(t)
	list = decoded(t, http.StatusOK, a.public("/public/booking/services"))
	require.Equal(t, false, list["bookingEnabled"])
	require.Equal(t, []any{}, list["items"])
}

func TestPublicAvailability(t *testing.T) {
	ctx := context.Background()
	_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM availability_rules")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pgtest.Pool(t).Exec(ctx, "DELETE FROM availability_rules")
		require.NoError(t, err)
	})
	_, err = booking.CreateRule(ctx, pgtest.Pool(t), booking.Rule{Weekday: 2, Start: "10:00", End: "13:00"})
	require.NoError(t, err)

	// The clock reads Monday 5 October 2026, 20:30 in Sydney, so the 24 hours'
	// notice empties Tuesday the 6th and leaves Tuesday the 13th.
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	svc := a.createService(t, token, "slots-for-visitors")
	path := "/public/availability?service=slots-for-visitors&from=2026-10-01&to=2026-10-14"

	got := decoded(t, http.StatusOK, a.public(path))
	require.Equal(t, []string{"days", "from", "service", "timezone", "to"}, keys(got))
	require.Equal(t, "2026-10-05", got["from"], "from is moved to today")
	require.Equal(t, "2026-10-14", got["to"])
	days := got["days"].([]any)
	require.Len(t, days, 1, "only days with slots")
	require.Equal(t, []string{"date", "slots"}, keys(days[0]))
	require.Equal(t, "2026-10-13", days[0].(map[string]any)["date"])
	slots := days[0].(map[string]any)["slots"].([]any)
	require.Equal(t, map[string]any{"startsAt": "2026-10-12T23:00:00Z", "endsAt": "2026-10-13T00:15:00Z"}, slots[0])
	require.Len(t, slots, 4)

	preview := decoded(t, http.StatusOK, a.send(http.MethodGet,
		"/admin/availability/preview?serviceId="+svc["id"].(string)+"&from=2026-10-01&to=2026-10-14", token))
	require.Equal(t, got["days"], preview["days"], "the admin previews exactly what visitors see")
	requireForbidden(t, a.send(http.MethodGet, "/admin/availability/preview?serviceId="+svc["id"].(string)+
		"&from=2026-10-01&to=2026-10-14", insertSession(t, a.clock.at, "content_editor")))

	// A request holding 10:00-11:15 with its buffer leaves only 11:30, and
	// the next read shows it: nothing is cached.
	bookOnce(t, svc["id"].(string), time.Date(2026, 10, 12, 23, 0, 0, 0, time.UTC), booking.Pending)
	days = decoded(t, http.StatusOK, a.public(path))["days"].([]any)
	require.Equal(t, []any{map[string]any{"startsAt": "2026-10-13T00:30:00Z", "endsAt": "2026-10-13T01:45:00Z"}},
		days[0].(map[string]any)["slots"])

	refused(t, http.StatusNotFound, "not_found", a.public("/public/availability?service=no-such-service&from=2026-10-06&to=2026-10-07"))
	refused(t, http.StatusUnprocessableEntity, "service_not_bookable",
		a.public("/public/availability?service=free-consultation&from=2026-10-06&to=2026-10-07"))
	p := refused(t, http.StatusBadRequest, "invalid_request", a.public("/public/availability?service=slots-for-visitors&from=2026-10-06&to=2026-12-08"))
	require.Equal(t, "to", p["errors"].([]any)[0].(map[string]any)["field"])
	requireUnauthenticated(t, a.send(http.MethodGet, path, token))

	pauseBooking(t)
	refused(t, http.StatusUnprocessableEntity, "booking_paused", a.public(path))
}
