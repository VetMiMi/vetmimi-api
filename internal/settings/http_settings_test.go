package settings_test

import (
	"maps"
	"net/http"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func settingsAPI(t *testing.T, h *settings.Handler) *webtest.API {
	h.Pool, h.Now = pgtest.Pool(t), webtest.Clock
	return webtest.New(t, h.Routes)
}

func TestHTTP_GetSettingsByRole(t *testing.T) {
	a := settingsAPI(t, &settings.Handler{})
	admin := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/settings", webtest.SignIn(t, "site_admin"), ""))
	require.Len(t, admin, 19, "18 keys and updatedAt")
	require.Equal(t, "Australia/Sydney", admin["timezone"])
	require.Equal(t, float64(48), admin["pendingHoldHours"])
	require.Equal(t, []any{"bank_transfer", "card"}, admin["paymentMethods"])
	require.Equal(t, map[string]any{"en": "Usually within 2 business days"}, admin["responseTime"])

	booking := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/settings", webtest.SignIn(t, "booking_admin"), ""))
	require.Len(t, booking, 19)

	editor := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/settings", webtest.SignIn(t, "content_editor"), ""))
	require.Equal(t, []string{"contactEmail", "responseTime", "retentionMonths", "timezone", "updatedAt"},
		slices.Sorted(maps.Keys(editor)), "the site keys only")

	webtest.Refused(t, http.StatusUnauthorized, "unauthenticated", a.Send(http.MethodGet, "/admin/settings", ""))
}

func TestHTTP_PatchSettings(t *testing.T) {
	keep(t, db.New(pgtest.Pool(t)))
	a := settingsAPI(t, &settings.Handler{})
	token := webtest.SignIn(t, "booking_admin")

	got := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPatch, "/admin/settings", token,
		`{"pendingHoldHours": 72, "paymentMethods": ["card"]}`))
	require.Equal(t, float64(72), got["pendingHoldHours"])
	require.Equal(t, []any{"card"}, got["paymentMethods"])
	require.NotEmpty(t, got["updatedAt"])

	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPatch, "/admin/settings", token, `{"reminderHours": 12, "timezone": "UTC"}`))
	webtest.Refused(t, http.StatusForbidden, "forbidden",
		a.Admin(http.MethodPatch, "/admin/settings", webtest.SignIn(t, "content_editor"), `{"retentionMonths": 12}`))
	after := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/settings", token, ""))
	require.Equal(t, float64(24), after["reminderHours"], "a refused patch saves nothing")
	require.Equal(t, "Australia/Sydney", after["timezone"])

	webtest.JSON(t, http.StatusOK, a.Admin(http.MethodPatch, "/admin/settings", webtest.SignIn(t, "site_admin"),
		`{"contactEmail": "hello@vetmimi.example", "responseTime": {"en": "Soon", "my": "မကြာမီ"}}`))
	require.NotContains(t, a.Logs.String(), "hello@vetmimi.example")
}

func TestHTTP_PatchSettingsRefusesBadBodies(t *testing.T) {
	a := settingsAPI(t, &settings.Handler{})
	token := webtest.SignIn(t, "site_admin")
	field := func(body string) []any {
		t.Helper()
		p := webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPatch, "/admin/settings", token, body))
		return p["errors"].([]any)
	}
	at := func(name, message string) []any { return []any{map[string]any{"field": name, "message": message}} }

	require.Equal(t, at("/timezone", "must be an IANA timezone name"), field(`{"timezone": "Mars/Olympus"}`))
	require.Equal(t, at("/pendingHoldHours", "must be at least 1"), field(`{"pendingHoldHours": 0}`))
	require.Equal(t, at("/pendingHoldHours", "must be of type number"), field(`{"pendingHoldHours": "72"}`))
	require.Equal(t, at("/paymentMethods", "must have at least 1 items"), field(`{"paymentMethods": []}`))
	require.Equal(t, at("/paymentMethods", "must not repeat an item"), field(`{"paymentMethods": ["card", "card"]}`))
	require.Equal(t, at("/paymentMethods/0", "must be one of: bank_transfer, card"), field(`{"paymentMethods": ["cash"]}`))
	require.Equal(t, at("/colour", "is not an allowed property"), field(`{"colour": "rose"}`))
	webtest.Refused(t, http.StatusBadRequest, "invalid_request", a.Admin(http.MethodPatch, "/admin/settings", token, `{}`))

	after := webtest.JSON(t, http.StatusOK, a.Admin(http.MethodGet, "/admin/settings", token, ""))
	require.Equal(t, "Australia/Sydney", after["timezone"])
	require.Equal(t, float64(48), after["pendingHoldHours"])
}
