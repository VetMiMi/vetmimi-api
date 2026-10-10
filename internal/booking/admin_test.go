package booking_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

// listFixture books eight appointments of a service of their own, one per
// day around now, and returns them by name.
func listFixture(t *testing.T) (map[string]db.Appointment, pgtype.UUID, time.Time) {
	t.Helper()
	ctx := context.Background()
	slug := bookService(t)
	svc, err := db.New(pgtest.Pool(t)).GetServiceBySlug(ctx, slug)
	require.NoError(t, err)

	plan := []struct {
		name   string
		status booking.Status
	}{
		{"expired", booking.Expired}, {"completed", booking.Completed}, {"pastConfirmed", booking.Confirmed},
		{"", ""}, // today, which now falls on
		{"laterHold", booking.Pending}, {"soonerHold", booking.Pending}, {"confirmed", booking.Confirmed},
		{"cancelled", booking.CancelledByPractitioner}, {"declined", booking.Declined},
	}
	var now time.Time
	out := map[string]db.Appointment{}
	for _, p := range plan {
		d := freeDay()
		start := time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, sydney)
		if p.name == "" {
			now = start.Add(2 * time.Hour)
			continue
		}
		a := appointment(t, p.status, start)
		a.Service = svc
		if p.name == "confirmed" {
			a.VisitorEmail = "special.case@example.com"
		}
		switch p.name {
		case "laterHold":
			a.HoldExpiresAt = sql.NullTime{Time: now.Add(time.Hour), Valid: true}
		case "soonerHold":
			a.HoldExpiresAt = sql.NullTime{Time: now.Add(30 * time.Minute), Valid: true}
		}
		appt, err := insert(t, a)
		require.NoError(t, err)
		out[p.name] = appt
	}
	return out, svc.ID, now
}

func listNames(t *testing.T, appts map[string]db.Appointment, f booking.Filter, now time.Time) ([]string, string) {
	t.Helper()
	page, err := booking.ListAppointments(context.Background(), db.New(pgtest.Pool(t)), f, now)
	require.NoError(t, err)
	require.Equal(t, "Australia/Sydney", page.Timezone)
	names := make([]string, len(page.Items))
	for i, item := range page.Items {
		for name, a := range appts {
			if a.ID == item.ID {
				names[i] = name
			}
		}
	}
	return names, page.NextCursor
}

func TestListAppointments_Views(t *testing.T) {
	appts, serviceID, now := listFixture(t)
	tests := map[booking.View][]string{
		"":                    {"laterHold", "soonerHold", "confirmed"},
		booking.ViewPending:   {"soonerHold", "laterHold"},
		booking.ViewPast:      {"declined", "cancelled", "pastConfirmed", "completed", "expired"},
		booking.ViewCancelled: {"declined", "cancelled"},
		booking.ViewAll: {"declined", "cancelled", "confirmed", "soonerHold", "laterHold", "pastConfirmed",
			"completed", "expired"},
	}
	for view, want := range tests {
		got, next := listNames(t, appts, booking.Filter{View: view, ServiceID: serviceID}, now)
		require.Equal(t, want, got, "view %q", view)
		require.Empty(t, next)
	}

	got, _ := listNames(t, appts, booking.Filter{View: booking.ViewAll, ServiceID: serviceID,
		Statuses: []booking.Status{booking.Confirmed}}, now)
	require.Equal(t, []string{"confirmed", "pastConfirmed"}, got, "a status filter narrows the view")
	got, _ = listNames(t, appts, booking.Filter{View: booking.ViewAll, ServiceID: serviceID,
		From: appts["pastConfirmed"].StartsAt, To: appts["confirmed"].StartsAt}, now)
	require.Equal(t, []string{"soonerHold", "laterHold", "pastConfirmed"}, got)
}

func TestListAppointments_Search(t *testing.T) {
	appts, serviceID, now := listFixture(t)
	ref := appts["completed"].Reference
	got, _ := listNames(t, appts, booking.Filter{View: booking.ViewAll, ServiceID: serviceID,
		Search: strings.ToLower(ref[3:])}, now)
	require.Equal(t, []string{"completed"}, got, "by reference, any case")
	got, _ = listNames(t, appts, booking.Filter{View: booking.ViewAll, ServiceID: serviceID, Search: "SPECIAL.CASE@"}, now)
	require.Equal(t, []string{"confirmed"}, got, "by email")
	got, _ = listNames(t, appts, booking.Filter{View: booking.ViewAll, ServiceID: serviceID, Search: "%"}, now)
	require.Empty(t, got, "wildcards are literal")
}

func TestListAppointments_CursorWalksEveryRowOnce(t *testing.T) {
	appts, serviceID, now := listFixture(t)
	f := booking.Filter{View: booking.ViewAll, ServiceID: serviceID, Limit: 3}
	var all []string
	for range 5 {
		page, next := listNames(t, appts, f, now)
		all = append(all, page...)
		if next == "" {
			break
		}
		f.Cursor = next
	}
	require.Equal(t, []string{"declined", "cancelled", "confirmed", "soonerHold", "laterHold", "pastConfirmed",
		"completed", "expired"}, all)

	_, err := booking.ListAppointments(context.Background(), db.New(pgtest.Pool(t)),
		booking.Filter{Cursor: "not-a-cursor"}, now)
	requireCode(t, apperr.InvalidRequest, err)
}

func TestGetAppointment_HistoryAndMessages(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	_, err := booking.Confirm(context.Background(), pgtest.Pool(t), testSecret, changeOf(t, appt), now)
	require.NoError(t, err)

	d, err := booking.GetAppointment(context.Background(), db.New(pgtest.Pool(t)), appt.ID, now)
	require.NoError(t, err)
	require.Equal(t, "confirmed", d.Appointment.Status)
	require.Equal(t, "individual-art-therapy", d.Appointment.ServiceSlug)
	require.Len(t, d.Events, 1)
	require.Equal(t, "confirmed", d.Events[0].Kind)
	require.Equal(t, "Daw Mi", d.Events[0].ActorName.String)
	require.Len(t, d.Communications, 2)
	require.Equal(t, []string{"reschedule", "cancel", "set_note", "mark_communicated"}, d.AllowedActions)

	_, err = booking.GetAppointment(context.Background(), db.New(pgtest.Pool(t)), practitioner(t), now)
	requireCode(t, apperr.NotFound, err)
}

func TestSetNote_RecordsLengthNotText(t *testing.T) {
	appt, now := booked(t, booking.Pending)
	require.NoError(t, booking.SetNote(context.Background(), pgtest.Pool(t), changeOf(t, appt), "Prefers mornings.", now))

	got := getAppointment(t, appt.ID)
	require.Equal(t, "Prefers mornings.", got.AdminNote.String)
	require.Equal(t, appt.Version+1, got.Version)
	require.Equal(t, []string{"note_updated", "", "", "admin", `{"length": 17}`}, lastEvent(t, appt.ID))

	err := booking.SetNote(context.Background(), pgtest.Pool(t), changeOf(t, appt), "Again.", now)
	requireCode(t, apperr.StaleVersion, err)
}

func TestAllowedActions(t *testing.T) {
	now := time.Date(2031, 3, 1, 9, 0, 0, 0, time.UTC)
	later, earlier := now.Add(time.Hour), now.Add(-time.Hour)
	tests := []struct {
		status booking.Status
		start  time.Time
		want   []string
	}{
		{booking.Pending, later, []string{"confirm", "decline", "reschedule"}},
		{booking.Confirmed, later, []string{"reschedule", "cancel"}},
		{booking.Confirmed, earlier, []string{"complete", "no_show"}},
		{booking.Confirmed, now, []string{"complete", "no_show"}},
		{booking.Declined, later, nil},
	}
	for _, tc := range tests {
		want := append(tc.want, "set_note", "mark_communicated")
		require.Equal(t, want, booking.AllowedActions(tc.status, tc.start, now), "%s at %s", tc.status, tc.start)
	}
}
