package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/platform/settings"
)

const settingsURL = "/admin/settings"

// keepSettings restores every setting when the test ends, since the tests in
// this package share one database.
func keepSettings(t *testing.T) {
	t.Helper()
	q := db.New(pgtest.Pool(t))
	before, err := settings.Load(context.Background(), q)
	require.NoError(t, err)
	t.Cleanup(func() {
		all, err := json.Marshal(before)
		require.NoError(t, err)
		var patch settings.Patch
		require.NoError(t, json.Unmarshal(all, &patch))
		_, err = settings.Update(context.Background(), q, patch, auth.User{Roles: []string{"site_admin"}}, time.Now())
		require.NoError(t, err)
	})
}

func (a *authAPI) patchSettings(token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPatch, settingsURL, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res := httptest.NewRecorder()
	a.handler.ServeHTTP(res, req)
	return res
}

func (a *authAPI) readSettings(t *testing.T, token string) map[string]any {
	t.Helper()
	res := a.send(http.MethodGet, settingsURL, token)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	return body
}

func TestSettingsEachRoleReads(t *testing.T) {
	a := newAuthAPI(t)
	admin := a.readSettings(t, insertSession(t, a.clock.at, "site_admin"))
	require.Len(t, admin, 19, "18 keys and updatedAt")
	require.Equal(t, "Australia/Sydney", admin["timezone"])
	require.Equal(t, float64(48), admin["pendingHoldHours"])
	require.Equal(t, []any{"bank_transfer", "card"}, admin["paymentMethods"])
	require.Equal(t, map[string]any{"en": "Usually within 2 business days"}, admin["responseTime"])

	require.Len(t, a.readSettings(t, insertSession(t, a.clock.at, "booking_admin")), 19)

	editor := a.readSettings(t, insertSession(t, a.clock.at, "content_editor"))
	require.ElementsMatch(t, []string{"timezone", "retentionMonths", "contactEmail", "responseTime", "updatedAt"}, keysOf(editor))
}

func TestBookingAdminChangesPendingHoldHours(t *testing.T) {
	keepSettings(t)
	a := newAuthAPI(t)
	userID, _ := createUser(t, []byte("sealed"), "booking_admin")
	token := sessionFor(t, userID, a.clock.at)

	res := a.patchSettings(token, `{"pendingHoldHours": 72}`)
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	var body map[string]any
	require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
	require.Equal(t, float64(72), body["pendingHoldHours"])
	require.NotEmpty(t, body["updatedAt"])
	require.Equal(t, float64(72), a.readSettings(t, token)["pendingHoldHours"])

	var by pgtype.UUID
	var at time.Time
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(),
		"SELECT updated_by, updated_at FROM settings WHERE key = 'pending_hold_hours'").Scan(&by, &at))
	require.Equal(t, userID, by)
	require.True(t, a.clock.at.Equal(at), "updated at the API's clock time")
}

// A booking administrator may not change site keys, and a patch mixing them
// saves nothing.
func TestBookingAdminCannotPatchSiteKeys(t *testing.T) {
	keepSettings(t)
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "booking_admin")
	requireForbidden(t, a.patchSettings(token, `{"reminderHours": 12, "timezone": "UTC"}`))

	after := a.readSettings(t, token)
	require.Equal(t, float64(24), after["reminderHours"])
	require.Equal(t, "Australia/Sydney", after["timezone"])
}

func TestSettingsPatchFailures(t *testing.T) {
	a := newAuthAPI(t)
	token := insertSession(t, a.clock.at, "site_admin")
	require.Equal(t, []map[string]any{fieldError("/timezone", "must be an IANA timezone name")},
		invalid(t, a.patchSettings(token, `{"timezone": "Mars/Olympus"}`)))
	require.NotEmpty(t, invalid(t, a.patchSettings(token, `{"pendingHoldHours": 0}`)))
	require.NotEmpty(t, invalid(t, a.patchSettings(token, `{}`)))
	require.NotEmpty(t, invalid(t, a.patchSettings(token, `{"colour": "rose"}`)))
	require.Equal(t, "Australia/Sydney", a.readSettings(t, token)["timezone"])
}
