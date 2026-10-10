package booking_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/video"
)

func manualBooking(t *testing.T, start time.Time, status booking.Status) booking.Manual {
	t.Helper()
	m := booking.Manual{
		IdempotencyKey: newKey(t), ServiceID: therapy(t).ID, StartsAt: start, Format: "online", Locale: "en",
		VisitorName: " Phoned In ", VisitorEmail: "Phone@Example.com", AdminNote: "Booked by phone.",
		Status: status, Notify: true, Actor: practitioner(t),
	}
	m.Body = fmt.Appendf(nil, "%s|%s|%v", m.StartsAt.UTC(), m.Status, m.Notify)
	return m
}

func createManual(t *testing.T, m booking.Manual, now time.Time) (booking.Created, error) {
	t.Helper()
	return booking.CreateManual(context.Background(), pgtest.Pool(t), testSecret, m, now)
}

func TestManual_ConfirmedTakesTheSlot(t *testing.T) {
	openEveryDay(t)
	setSetting(t, "public_booking_enabled", "false")
	start, now := slotStart()
	created := counter("appointments_created", "manual")
	got, err := createManual(t, manualBooking(t, start, booking.Confirmed), now)
	require.NoError(t, err, "pausing public booking does not stop Daw Mi")
	require.Equal(t, created+1, counter("appointments_created", "manual"))

	appt := getAppointment(t, got.AppointmentID)
	require.Equal(t, "confirmed", appt.Status)
	require.Equal(t, "manual", appt.Source)
	require.Equal(t, "Phoned In", appt.VisitorName)
	require.Equal(t, "phone@example.com", appt.VisitorEmail)
	require.Equal(t, "Booked by phone.", appt.AdminNote.String)
	require.Equal(t, practitioner(t), appt.CreatedBy)
	require.False(t, appt.PrivacyAckAt.Valid)
	require.Equal(t, []string{"created", "", "confirmed", "admin", `{"source": "manual"}`}, lastEvent(t, appt.ID))
	require.Equal(t, []string{"booking_confirmed:queued:", "reminder:queued:"}, commStatuses(t, appt.ID),
		"no practitioner email: she made it")
	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver, video.TaskCloseRoom}, taskTypes(got.Tasks))
	_, hasRoom := roomOf(t, appt.ID)
	require.True(t, hasRoom, "a confirmed online booking gets its room")

	setSetting(t, "public_booking_enabled", "true")
	local := start.In(sydney)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	free, err := booking.Slots(context.Background(), db.New(pgtest.Pool(t)), therapy(t), day, day, now)
	require.NoError(t, err)
	for _, d := range free.Days {
		for _, slot := range d.Slots {
			require.False(t, slot.Start.Equal(start), "the slot is no longer public")
		}
	}
}

func TestManual_WithoutNoticeOnlyTheReminder(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	m := manualBooking(t, start, booking.Confirmed)
	m.Notify = false
	got, err := createManual(t, m, now)
	require.NoError(t, err)
	require.Equal(t, []string{"reminder:queued:"}, commStatuses(t, got.AppointmentID))
}

func TestManual_PendingHoldsLikeARequest(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	got, err := createManual(t, manualBooking(t, start, booking.Pending), now)
	require.NoError(t, err)
	appt := getAppointment(t, got.AppointmentID)
	require.Equal(t, "pending", appt.Status)
	require.True(t, now.Add(48*time.Hour).Equal(appt.HoldExpiresAt.Time))
	require.Equal(t, []string{"request_received:queued:"}, commStatuses(t, appt.ID))
	require.Equal(t, []string{booking.TaskExpireHold, comms.TaskDeliver}, taskTypes(got.Tasks))
}

func TestManual_ReplaySameKey(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	m := manualBooking(t, start, booking.Confirmed)
	first, err := createManual(t, m, now)
	require.NoError(t, err)
	again, err := createManual(t, m, now)
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Equal(t, first.AppointmentID, again.AppointmentID)
	require.Empty(t, again.Tasks)

	m.Body = append(m.Body, '!')
	_, err = createManual(t, m, now)
	requireCode(t, apperr.IdempotencyKeyReused, err)
}

func TestManual_Refusals(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	_, err := createManual(t, manualBooking(t, start, booking.Confirmed), now)
	require.NoError(t, err)
	paused, err := db.New(pgtest.Pool(t)).GetServiceBySlug(context.Background(), "free-consultation")
	require.NoError(t, err)

	tests := map[string]struct {
		change func(*booking.Manual)
		code   apperr.Code
	}{
		"taken slot":            {func(m *booking.Manual) { m.StartsAt = start.Add(30 * time.Minute) }, apperr.SlotUnavailable},
		"outside opening hours": {func(m *booking.Manual) { m.StartsAt = start.Add(8 * time.Hour) }, apperr.SlotUnavailable},
		"inside the notice":     {func(m *booking.Manual) { m.StartsAt = now.Add(time.Hour) }, apperr.OutsideBookingWindow},
		"paused service":        {func(m *booking.Manual) { m.ServiceID = paused.ID }, apperr.ServiceNotBookable},
		"unknown service":       {func(m *booking.Manual) { m.ServiceID = practitioner(t) }, apperr.NotFound},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			m := manualBooking(t, start.Add(2*time.Hour), booking.Confirmed)
			tc.change(&m)
			_, err := createManual(t, m, now)
			requireCode(t, tc.code, err)
		})
	}
}

// ADR-004: Daw Mi books a phone caller into the time a visitor is
// requesting online at the same moment; exactly one gets it.
func TestManual_RacesPublicRequest(t *testing.T) {
	openEveryDay(t)
	for range 5 {
		start, now := slotStart()
		created := counter("appointments_created", "manual") + counter("appointments_created", "website")
		conflicts := counter("slot_conflicts", "manual") + counter("slot_conflicts", "website")
		errs := make(chan error, 2)
		go func() {
			_, err := createManual(t, manualBooking(t, start, booking.Confirmed), now)
			errs <- err
		}()
		go func() {
			_, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
			errs <- err
		}()
		a, b := <-errs, <-errs
		if a != nil {
			a, b = b, a
		}
		require.NoError(t, a)
		requireCode(t, apperr.SlotUnavailable, b)
		require.Equal(t, created+1, counter("appointments_created", "manual")+counter("appointments_created", "website"))
		require.Equal(t, conflicts+1, counter("slot_conflicts", "manual")+counter("slot_conflicts", "website"))
	}
}
