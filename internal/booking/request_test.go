package booking_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/platform/pgtest"
)

// openEveryDay opens the practice 09:00-17:00 every weekday for the test.
func openEveryDay(t *testing.T) {
	t.Helper()
	clearRules(t)
	for weekday := int16(1); weekday <= 7; weekday++ {
		_, err := createRule(t, weekday, "09:00", "17:00")
		require.NoError(t, err)
	}
}

// setSetting stores value under key until the test ends.
func setSetting(t *testing.T, key, value string) {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.Pool(t)
	var old string
	require.NoError(t, pool.QueryRow(ctx, "SELECT value::text FROM settings WHERE key = $1", key).Scan(&old))
	_, err := pool.Exec(ctx, "UPDATE settings SET value = $2::jsonb WHERE key = $1", key, value)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "UPDATE settings SET value = $2::jsonb WHERE key = $1", key, old)
		require.NoError(t, err)
	})
}

// slotStart is 10:00 in Sydney on a day no other test uses, and now is
// three days before it, inside the booking window.
func slotStart() (start, now time.Time) {
	d := freeDay()
	start = time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, sydney)
	return start, start.Add(-72 * time.Hour)
}

func visitorRequest(key, service string, start time.Time) booking.Request {
	r := booking.Request{
		IdempotencyKey: key, Service: service, StartsAt: start, Format: "online", Locale: "my",
		VisitorName: "  Visitor ", VisitorEmail: " Visitor@Example.com", VisitorNote: "Ground floor, please.",
		PrivacyAcknowledged: true, PolicyAcknowledged: true,
	}
	r.Body = []byte(fmt.Sprintf("%s|%s|%s", r.Service, r.StartsAt.UTC(), r.Format))
	return r
}

func request(t *testing.T, r booking.Request, now time.Time) (booking.Requested, error) {
	t.Helper()
	practitioner(t)
	return booking.RequestAppointment(context.Background(), pgtest.Pool(t), testSecret, r, now)
}

// bookService creates an active service whose action is book, so instant
// mode applies to it, and removes it and its appointments afterwards.
func bookService(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	pool := pgtest.Pool(t)
	svc, err := booking.CreateService(ctx, db.New(pool), db.CreateServiceParams{
		Slug: "instant-session", Name: []byte(`{"en": "Instant session"}`), BookingAction: "book",
		DurationMinutes: pgtype.Int4{Int32: 60, Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := pool.Exec(ctx, "DELETE FROM appointments WHERE service_id = $1", svc.ID)
		require.NoError(t, err)
		_, err = pool.Exec(ctx, "DELETE FROM services WHERE id = $1", svc.ID)
		require.NoError(t, err)
	})
	return svc.Slug
}

func newKey(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("%s-%d", t.Name(), time.Now().UnixNano())
}

func taskTypes(tasks []platform.Task) []string {
	out := make([]string, len(tasks))
	for i, task := range tasks {
		out[i] = task.Type
	}
	return out
}

func commKinds(t *testing.T, id pgtype.UUID) []string {
	t.Helper()
	rows, err := pgtest.Pool(t).Query(context.Background(),
		"SELECT kind || ':' || recipient || ':' || locale FROM communications WHERE appointment_id = $1 ORDER BY kind", id)
	require.NoError(t, err)
	var kinds []string
	for rows.Next() {
		var k string
		require.NoError(t, rows.Scan(&k))
		kinds = append(kinds, k)
	}
	require.NoError(t, rows.Err())
	return kinds
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pgtest.Pool(t).QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func getAppointment(t *testing.T, id pgtype.UUID) db.Appointment {
	t.Helper()
	tx, err := pgtest.Pool(t).Begin(context.Background())
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(context.Background()) }()
	appt, err := db.New(tx).LockAppointment(context.Background(), id)
	require.NoError(t, err)
	return appt
}

func TestRequest_StoresPendingWithHold(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	got, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)

	require.Equal(t, booking.Pending, got.Receipt.Status)
	require.Regexp(t, referencePattern, got.Receipt.Reference)
	require.Equal(t, booking.ReceiptService{Slug: "individual-art-therapy", Name: "တစ်ဦးချင်း အနုပညာကုထုံး"},
		got.Receipt.Service)
	require.True(t, start.Equal(got.Receipt.StartsAt))
	require.Equal(t, 60, got.Receipt.DurationMinutes)

	appt := getAppointment(t, got.AppointmentID)
	require.Equal(t, "pending", appt.Status)
	require.Equal(t, "Visitor", appt.VisitorName)
	require.Equal(t, "visitor@example.com", appt.VisitorEmail)
	require.Equal(t, "website", appt.Source)
	require.True(t, now.Add(48*time.Hour).Equal(appt.HoldExpiresAt.Time), "hold is now + pending_hold_hours")
	require.Equal(t, []string{"practitioner_new_request:meenaerie@gmail.com:en", "request_received:visitor@example.com:my"},
		commKinds(t, appt.ID))
	require.Equal(t, 1, countRows(t,
		"SELECT count(*) FROM appointment_events WHERE appointment_id = $1 AND kind = 'created' AND actor = 'visitor'", appt.ID))

	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver, booking.TaskExpireHold}, taskTypes(got.Tasks))
	require.Equal(t, "hold:"+appt.ID.String(), got.Tasks[2].ID)
	require.True(t, appt.HoldExpiresAt.Time.Equal(got.Tasks[2].ProcessAt))
}

func TestRequest_HoldEndsAtStartWhenSooner(t *testing.T) {
	openEveryDay(t)
	start, _ := slotStart()
	now := start.Add(-30 * time.Hour)
	got, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)
	require.True(t, start.Equal(getAppointment(t, got.AppointmentID).HoldExpiresAt.Time))
}

func TestRequest_ReplaySameKey(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	r := visitorRequest(newKey(t), "individual-art-therapy", start)
	first, err := request(t, r, now)
	require.NoError(t, err)

	again, err := request(t, r, now.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, again.Replayed)
	require.Empty(t, again.Tasks)
	require.Equal(t, first.AppointmentID, again.AppointmentID)
	require.Equal(t, first.Receipt.Reference, again.Receipt.Reference)
	require.True(t, first.Receipt.StartsAt.Equal(again.Receipt.StartsAt))
	require.Equal(t, 1, countRows(t, "SELECT count(*) FROM appointments WHERE starts_at = $1", start))

	other := visitorRequest(r.IdempotencyKey, "individual-art-therapy", start.Add(time.Hour))
	_, err = request(t, other, now)
	requireCode(t, apperr.IdempotencyKeyReused, err)
}

func TestRequest_Refusals(t *testing.T) {
	openEveryDay(t)
	start, now := slotStart()
	_, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)

	e := requireCode(t, apperr.SlotUnavailable, func() error {
		_, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start.Add(30*time.Minute)), now)
		return err
	}())
	alternatives := e.Extensions["alternatives"]
	encoded, err := json.Marshal(alternatives)
	require.NoError(t, err)
	var slots []map[string]time.Time
	require.NoError(t, json.Unmarshal(encoded, &slots))
	require.Len(t, slots, 5)
	require.True(t, start.Add(90*time.Minute).Equal(slots[0]["startsAt"]),
		"nearby free slots, none overlapping the stored request or its buffer")

	tests := map[string]struct {
		change func(*booking.Request)
		code   apperr.Code
	}{
		"not on the slot grid":  {func(r *booking.Request) { r.StartsAt = r.StartsAt.Add(10 * time.Minute) }, apperr.SlotUnavailable},
		"outside opening hours": {func(r *booking.Request) { r.StartsAt = r.StartsAt.Add(8 * time.Hour) }, apperr.SlotUnavailable},
		"inside the notice":     {func(r *booking.Request) { r.StartsAt = now.Add(time.Hour) }, apperr.OutsideBookingWindow},
		"beyond the window":     {func(r *booking.Request) { r.StartsAt = r.StartsAt.AddDate(0, 0, 61) }, apperr.OutsideBookingWindow},
		"paused service":        {func(r *booking.Request) { r.Service = "free-consultation" }, apperr.ServiceNotBookable},
		"unknown service":       {func(r *booking.Request) { r.Service = "no-such-service" }, apperr.ServiceNotBookable},
		"format not offered":    {func(r *booking.Request) { r.Format = "in_person" }, apperr.ActionNotAllowed},
		"policy not acknowledged": {func(r *booking.Request) { r.PolicyAcknowledged = false },
			apperr.AcknowledgementRequired},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r := visitorRequest(newKey(t), "individual-art-therapy", start.Add(2*time.Hour))
			tc.change(&r)
			_, err := request(t, r, now)
			requireCode(t, tc.code, err)
		})
	}

	setSetting(t, "public_booking_enabled", "false")
	_, err = request(t, visitorRequest(newKey(t), "individual-art-therapy", start.Add(2*time.Hour)), now)
	requireCode(t, apperr.BookingPaused, err)
	require.Equal(t, 1, countRows(t, "SELECT count(*) FROM idempotency_keys WHERE key LIKE $1", t.Name()+"%"),
		"only the stored request keeps its key; refused ones roll theirs back")
}

// ADR-004: two visitors, different keys, one slot, at once: exactly one
// stores a request and the other is told the slot is gone.
func TestRequest_TwoVisitorsOneSlot(t *testing.T) {
	openEveryDay(t)
	service := bookService(t)
	for _, mode := range []string{"request_approval", "instant"} {
		setSetting(t, "booking_mode", fmt.Sprintf("%q", mode))
		for range 10 {
			start, now := slotStart()
			errs := make([]error, 2)
			var wg sync.WaitGroup
			ready := make(chan struct{})
			for i := range errs {
				wg.Go(func() {
					<-ready
					_, errs[i] = request(t, visitorRequest(newKey(t)+fmt.Sprint(i), service, start), now)
				})
			}
			close(ready)
			wg.Wait()
			failed := 0
			for _, err := range errs {
				if err != nil {
					failed++
					requireCode(t, apperr.SlotUnavailable, err)
				}
			}
			require.Equal(t, 1, failed, "exactly one visitor wins")
		}
	}
}

func TestInstant_BookServiceIsConfirmed(t *testing.T) {
	openEveryDay(t)
	service := bookService(t)
	start, now := slotStart()
	pending, err := request(t, visitorRequest(newKey(t), service, start.Add(4*time.Hour)), now)
	require.NoError(t, err)
	setSetting(t, "booking_mode", `"instant"`)

	got, err := request(t, visitorRequest(newKey(t), service, start), now)
	require.NoError(t, err)
	require.Equal(t, booking.Confirmed, got.Receipt.Status)
	appt := getAppointment(t, got.AppointmentID)
	require.Equal(t, "confirmed", appt.Status)
	require.False(t, appt.HoldExpiresAt.Valid, "a confirmed appointment holds nothing")
	require.Equal(t, []string{"booking_confirmed:visitor@example.com:my",
		"practitioner_new_booking:meenaerie@gmail.com:en", "reminder:visitor@example.com:my"}, commKinds(t, appt.ID))
	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver, comms.TaskDeliver}, taskTypes(got.Tasks),
		"no hold to expire")
	require.Equal(t, "pending", getAppointment(t, pending.AppointmentID).Status,
		"switching the mode leaves earlier requests alone")
}

func TestInstant_RequestServiceStaysPending(t *testing.T) {
	openEveryDay(t)
	setSetting(t, "booking_mode", `"instant"`)
	start, now := slotStart()
	got, err := request(t, visitorRequest(newKey(t), "individual-art-therapy", start), now)
	require.NoError(t, err)
	require.Equal(t, booking.Pending, got.Receipt.Status)
	require.Equal(t, []string{comms.TaskDeliver, comms.TaskDeliver, booking.TaskExpireHold}, taskTypes(got.Tasks))
}
