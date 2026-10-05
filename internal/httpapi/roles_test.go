package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// Only the auth tag is generated yet, so most of the table is tested against
// the middleware directly, with requests routed to the operations in
// openapi.yaml; the generated routes test it end to end.

func requireForbidden(t *testing.T, res *httptest.ResponseRecorder) {
	t.Helper()
	require.Equal(t, http.StatusForbidden, res.Code, res.Body.String())
	require.Equal(t, "forbidden", problemFrom(t, res)["code"])
}

// The whole contract: every signed-in operation has roles, and the table
// names nothing else, so a renamed or removed operation cannot leave a stale
// row behind.
func TestEverySessionOperationHasRoles(t *testing.T) {
	spec := load(t, "../../openapi.yaml")
	signedIn := map[string]bool{}
	for path, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			scheme, err := securityOf(op)
			require.NoError(t, err, "%s %s (%s)", method, path, op.OperationID)
			if scheme == schemeSessionToken {
				signedIn[op.OperationID] = true
			}
		}
	}
	t.Logf("%d signed-in operations", len(signedIn))
	for id := range signedIn {
		require.NotEmpty(t, rolesByOperation[id], "%s is a sessionToken operation with no roles", id)
	}
	for id := range rolesByOperation {
		require.True(t, signedIn[id], "%s has roles but is not a sessionToken operation", id)
	}
}

// Content editors never reach booking data and booking administrators never
// edit content: the only operations open to both are signing out, reading
// oneself and reading settings, whose keys the settings domain filters.
func TestOnlyAuthAndSettingsReadAreSharedByEditorsAndBookingAdmins(t *testing.T) {
	shared := map[string]bool{}
	reach := map[auth.Role]int{}
	for id, allowed := range rolesByOperation {
		editor := permits(allowed, []string{string(auth.ContentEditor)})
		booking := permits(allowed, []string{string(auth.BookingAdmin)})
		if editor && booking {
			shared[id] = true
		}
		for _, r := range everyRole {
			if permits(allowed, []string{string(r)}) {
				reach[r]++
			}
		}
	}
	t.Logf("operations each role alone reaches: %v of %d", reach, len(rolesByOperation))
	require.Equal(t, map[string]bool{"deleteCurrentSession": true, "getCurrentUser": true, "getSettings": true}, shared)
}

// roleCheck is the role check over the whole contract, not only the
// generated tags, with each signed-in operation's method and route pattern.
type roleCheck struct {
	check  gen.MiddlewareFunc
	routes map[string][2]string
}

func newRoleCheck(t *testing.T) roleCheck {
	t.Helper()
	ops := index(t, load(t, "../../openapi.yaml"))
	rc := roleCheck{check: requireRoles(ops), routes: map[string][2]string{}}
	for key, op := range ops {
		if op.Security == schemeSessionToken {
			method, pattern, _ := strings.Cut(key, " ")
			rc.routes[op.ID] = [2]string{method, pattern}
		}
	}
	require.NotEmpty(t, rc.routes)
	return rc
}

// as serves a request for the operation id names, routed as chi would route
// it, from a user holding roles, through the role check alone.
func (rc roleCheck) as(t *testing.T, id string, roles ...string) *httptest.ResponseRecorder {
	t.Helper()
	route, ok := rc.routes[id]
	require.True(t, ok, "%s is not a signed-in operation", id)
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{route[1]}
	ctx := context.WithValue(context.Background(), chi.RouteCtxKey, rctx)
	ctx = auth.WithSession(ctx, auth.Session{User: auth.User{Roles: roles}})
	req := httptest.NewRequest(route[0], "/", nil).WithContext(ctx)

	res := httptest.NewRecorder()
	rc.check(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(res, req)
	return res
}

func TestSiteAdminPassesEveryRow(t *testing.T) {
	rc := newRoleCheck(t)
	for id := range rc.routes {
		require.Equal(t, http.StatusOK, rc.as(t, id, "site_admin").Code, id)
	}
	require.Len(t, rc.routes, len(rolesByOperation))
}

func TestEachRolePassesItsOwnRows(t *testing.T) {
	rc := newRoleCheck(t)
	for id, roles := range map[string][]string{
		"getCurrentUser":        {"content_editor"},
		"deleteCurrentSession":  {"booking_admin"},
		"getSettings":           {"content_editor"},
		"updateSettings":        {"booking_admin"},
		"listAppointments":      {"booking_admin"},
		"getContactEnquiry":     {"booking_admin"},
		"startVideoSession":     {"booking_admin"},
		"updateStory":           {"content_editor"},
		"createPageSection":     {"content_editor"},
		"uploadMedia":           {"content_editor"},
		"setFacebookPostStatus": {"content_editor"},
		"publishStory":          {"booking_admin", "site_admin"},
	} {
		require.Equal(t, http.StatusOK, rc.as(t, id, roles...).Code, "%s as %v", id, roles)
	}
}

// A refusal answers the same bytes whatever was refused, so it names no
// operation, route or resource (Booking & Admin UX, section 29).
func TestContentEditorIsRefusedBookingWithNoResourceData(t *testing.T) {
	rc := newRoleCheck(t)
	var first string
	for _, id := range []string{"getAppointment", "getContactEnquiry", "listAppointments", "updateSettings", "getBookingDashboard"} {
		res := rc.as(t, id, "content_editor")
		requireForbidden(t, res)
		var body map[string]any
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &body))
		require.ElementsMatch(t, []string{"type", "title", "status", "code", "detail"}, keysOf(body), id)
		lower := strings.ToLower(res.Body.String())
		for _, leak := range []string{strings.ToLower(id), "appointment", "enquir", "setting", "dashboard", "booking", "admin"} {
			require.NotContains(t, lower, leak, id)
		}
		if first == "" {
			first = res.Body.String()
			continue
		}
		require.Equal(t, first, res.Body.String(), id)
	}
}

func keysOf(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func TestBookingAdminCannotPublishStory(t *testing.T) {
	rc := newRoleCheck(t)
	requireForbidden(t, rc.as(t, "publishStory", "booking_admin"))
	requireForbidden(t, rc.as(t, "publishStory", "content_editor"))
	requireForbidden(t, rc.as(t, "publishStory", "content_editor", "booking_admin"))
	require.Equal(t, http.StatusOK, rc.as(t, "publishStory", "site_admin").Code)
}

func TestSiteAdminOnlyRowsRefuseTheOtherRoles(t *testing.T) {
	rc := newRoleCheck(t)
	for _, id := range []string{"approvePage", "deleteMedia", "replaceFeaturedArea", "getFeaturedArea", "archiveStory"} {
		requireForbidden(t, rc.as(t, id, "content_editor", "booking_admin"))
	}
}

func TestNoRolesOrNoSessionIsForbidden(t *testing.T) {
	rc := newRoleCheck(t)
	requireForbidden(t, rc.as(t, "getCurrentUser"))
	requireForbidden(t, rc.as(t, "getCurrentUser", "admin"))
}

// End to end on the real router: every role reads itself and signs out.
func TestContentEditorReadsTheCurrentUser(t *testing.T) {
	api := NewRouter(Deps{Log: quiet, Sessions: newSessions(t, time.Now)})
	send := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		return res
	}
	for _, role := range []string{"content_editor", "booking_admin", "site_admin"} {
		token := insertSession(t, time.Now(), role)
		res := send(http.MethodGet, "/auth/me", token)
		require.Equal(t, http.StatusOK, res.Code, "%s: %s", role, res.Body.String())
		var me map[string]any
		require.NoError(t, json.Unmarshal(res.Body.Bytes(), &me))
		require.Equal(t, []any{role}, me["roles"])
		require.Equal(t, http.StatusNoContent, send(http.MethodDelete, "/auth/sessions/current", token).Code, role)
	}
}

// rolesAPI serves testdata/roles.yaml through mountAPI, counting in
// REDIS_URL_TEST.
func rolesAPI(t *testing.T) *limitedAPI {
	t.Helper()
	rdb, prefix := liveRedis(t)
	return serveSpec(t, rdb, prefix, load(t, "testdata/roles.yaml"), nil)
}

func (a *limitedAPI) sessionAs(t *testing.T, roles ...string) string {
	t.Helper()
	return insertSession(t, a.clock.at, roles...)
}

// An operation the table does not name is closed to everyone, Daw Mi
// included, and answers the same bytes as any other refusal; operations
// that are not signed in are left alone.
func TestOperationMissingFromTheTableIsForbidden(t *testing.T) {
	require.NotContains(t, rolesByOperation, "notInTheRoleTable")
	require.NotContains(t, rolesByOperation, "getHealthz")
	require.NotContains(t, rolesByOperation, "getReadyz")
	a := serveSpec(t, nil, "", load(t, "testdata/roles.yaml"), nil)
	everything := a.sessionAs(t, "content_editor", "booking_admin", "site_admin")
	booking := a.sessionAs(t, "booking_admin")

	missing := a.send(http.MethodDelete, "/auth/sessions/current", bearer(everything)...)
	requireForbidden(t, missing)
	refused := a.send(http.MethodGet, meURL, bearer(booking)...)
	requireForbidden(t, refused)
	require.Equal(t, refused.Body.String(), missing.Body.String())

	require.Equal(t, http.StatusOK, a.send(http.MethodGet, meURL, bearer(everything)...).Code)
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, "/healthz", "X-Service-Key", testServiceKey).Code)
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, "/readyz").Code)
}

// The role check runs after the session check, which loads the roles, and
// before the rate limit and validation: a signed-in caller without the role
// learns nothing about the contract and is never counted.
func TestRolesAreCheckedAfterTheSessionAndBeforeTheLimit(t *testing.T) {
	a := rolesAPI(t)
	booking := a.sessionAs(t, "booking_admin")
	admin := a.sessionAs(t, "site_admin")

	// Session before roles: no session is 401, not 403.
	requireUnauthenticated(t, a.send(http.MethodGet, meURL))

	// Roles before the limit: refused, and not counted.
	for range 10 {
		requireForbidden(t, a.send(http.MethodGet, meURL, bearer(booking)...))
	}
	require.Empty(t, a.keys(t))

	// Roles before validation: a malformed call is 403, not 400.
	requireForbidden(t, a.send(http.MethodGet, "/auth/me", bearer(booking)...))
	require.Empty(t, a.keys(t))

	// With the role, the call is counted, then validated.
	require.Equal(t, http.StatusOK, a.send(http.MethodGet, meURL, bearer(admin)...).Code)
	require.Len(t, a.keys(t), 1)
	require.Equal(t, []map[string]any{fieldError("probe", "is required")},
		invalid(t, a.send(http.MethodGet, "/auth/me", bearer(admin)...)))
}
