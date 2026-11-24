package booking_test

import (
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/webtest"
)

func TestHTTP_ExportAppointments(t *testing.T) {
	day := freeDay()
	store := func(hour int, name string) db.Appointment {
		a := appointment(t, booking.Confirmed, at(day.Year(), day.Month(), day.Day(), hour, 0))
		a.VisitorName = name
		a.VisitorPhone = pgtype.Text{String: "0412 345 678", Valid: true}
		a.VisitorNote = pgtype.Text{String: "Ground floor, ring twice", Valid: true}
		a.AdminNote = pgtype.Text{String: "Private family matter", Valid: true}
		appt, err := insert(t, a)
		require.NoError(t, err)
		return appt
	}
	// Stored out of start order, so the file's order comes from the export.
	afternoon := store(15, "Visitor")
	morning := store(9, "မောင်မောင်")
	noon := store(12, "Visitor")
	a := bookingAPI(t)
	on := day.Format(time.DateOnly)

	res := a.Admin(http.MethodGet, "/admin/appointments/export?from="+on+"&to="+on, webtest.SignIn(t, "booking_admin"), "")
	rows := csvRows(t, res)
	require.Equal(t, "text/csv; charset=utf-8", res.Header().Get("Content-Type"))
	require.Equal(t, `attachment; filename="vetmimi-appointments-`+on+`-`+on+`.csv"`, res.Header().Get("Content-Disposition"))
	require.Equal(t, "no-store", res.Header().Get("Cache-Control"))
	require.Equal(t, []string{"Reference", "Status", "Service", "Date", "Start time", "Timezone", "Duration (minutes)",
		"Format", "Client name", "Email", "Phone", "Practical note", "Late cancellation", "Created", "Last updated"}, rows[0])
	require.Equal(t, []string{
		morning.Reference, "confirmed", "Individual Art Therapy", on, "09:00", "Australia/Sydney", "60", "online",
		"မောင်မောင်", "visitor@example.com", "0412 345 678", "Ground floor, ring twice", "no",
		morning.CreatedAt.In(sydney).Format(time.DateTime), morning.UpdatedAt.In(sydney).Format(time.DateTime),
	}, rows[1])
	require.Equal(t, []string{noon.Reference, "12:00"}, []string{rows[2][0], rows[2][4]})
	require.Equal(t, []string{afternoon.Reference, "15:00"}, []string{rows[3][0], rows[3][4]})
	require.Len(t, rows, 4)
	require.NotContains(t, res.Body.String(), "Private family matter")

	require.Contains(t, a.Logs.String(), `"msg":"appointments_exported"`)
	require.Contains(t, a.Logs.String(), `"from":"`+on+`","to":"`+on+`","rows":3`)
	require.NotContains(t, a.Logs.String(), "visitor@example.com")
	require.NotContains(t, a.Logs.String(), "မောင်မောင်")
}

func TestHTTP_ExportAppointmentsRefusals(t *testing.T) {
	a := bookingAPI(t)
	token := webtest.SignIn(t, "booking_admin")

	webtest.Refused(t, http.StatusForbidden, "forbidden", a.Admin(http.MethodGet,
		"/admin/appointments/export?from=2026-01-01&to=2026-01-31", webtest.SignIn(t, "content_editor"), ""))
	p := webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/appointments/export?from=2026-02-01&to=2026-01-31", token, ""))
	require.Equal(t, []any{map[string]any{"field": "to", "message": "must not be before from"}}, p["errors"])
	p = webtest.Refused(t, http.StatusBadRequest, "invalid_request",
		a.Admin(http.MethodGet, "/admin/appointments/export?from=2026-01-01&to=2027-01-03", token, ""))
	require.Equal(t, []any{map[string]any{"field": "to", "message": "must be at most 366 days after from"}}, p["errors"])

	res := a.Admin(http.MethodGet, "/admin/appointments/export?from=2026-01-01&to=2027-01-02", webtest.SignIn(t, "site_admin"), "")
	require.Equal(t, http.StatusOK, res.Code, "366 days after from is allowed, and a site admin may export")
}

func TestHTTP_ExportEscapesFormulas(t *testing.T) {
	day := freeDay()
	appt := appointment(t, booking.Confirmed, at(day.Year(), day.Month(), day.Day(), 10, 0))
	appt.VisitorName = `=HYPERLINK("https://example.com","Open")`
	_, err := insert(t, appt)
	require.NoError(t, err)
	on := day.Format(time.DateOnly)

	res := bookingAPI(t).Admin(http.MethodGet, "/admin/appointments/export?from="+on+"&to="+on,
		webtest.SignIn(t, "booking_admin"), "")
	require.Equal(t, `'=HYPERLINK("https://example.com","Open")`, csvRows(t, res)[1][8])
}

// csvRows checks the export answered 200 with a byte-order mark and returns its rows, header first.
func csvRows(t *testing.T, res *httptest.ResponseRecorder) [][]string {
	t.Helper()
	require.Equal(t, http.StatusOK, res.Code, res.Body.String())
	body, found := strings.CutPrefix(res.Body.String(), "\uFEFF")
	require.True(t, found, "the file starts with a byte-order mark")
	rows, err := csv.NewReader(strings.NewReader(body)).ReadAll()
	require.NoError(t, err)
	return rows
}
