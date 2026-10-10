package settings_test

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

// defaults are the provisional values docs/data-model.md, "Settings keys",
// lists and the migration seeds.
var defaults = settings.Settings{
	Timezone:                    "Australia/Sydney",
	BookingMode:                 "request_approval",
	PublicBookingEnabled:        true,
	PendingHoldHours:            48,
	ReminderHours:               24,
	MinNoticeHours:              24,
	MaxAdvanceDays:              60,
	SlotStepMinutes:             30,
	CancellationNoticeHours:     48,
	LateCancellationFeePercent:  50,
	LateCancellationFirstWaived: true,
	NoShowFeePercent:            100,
	MeetingLinkMode:             "vetmimi_room",
	PaymentMethods:              []string{"bank_transfer", "card"},
	InvoiceTiming:               "after_session",
	RetentionMonths:             24,
	ContactEmail:                "meenaerie@gmail.com",
	ResponseTime:                settings.Localized{En: "Usually within 2 business days"},
}

var siteAdmin = auth.User{Roles: []string{"site_admin"}}

// requireSame compares the values, not the unexported change times.
func requireSame(t *testing.T, want, got settings.Settings) {
	t.Helper()
	w, err := json.Marshal(want)
	require.NoError(t, err)
	g, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(w), string(g))
}

// keep restores every setting when the test ends, since tests share one
// database.
func keep(t *testing.T, q db.Querier) {
	t.Helper()
	before, err := settings.Load(context.Background(), q)
	require.NoError(t, err)
	t.Cleanup(func() {
		all, err := json.Marshal(before)
		require.NoError(t, err)
		var patch settings.Patch
		require.NoError(t, json.Unmarshal(all, &patch))
		_, err = settings.Update(context.Background(), q, patch, siteAdmin, time.Now())
		require.NoError(t, err)
	})
}

func TestSeededRowsAreTheDefaults(t *testing.T) {
	got, err := settings.Load(context.Background(), db.New(pgtest.Pool(t)))
	require.NoError(t, err)
	requireSame(t, defaults, got)
}

func TestUpdateChecksRangesWithoutHTTP(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	_, err := settings.Update(context.Background(), q,
		settings.Patch{"pending_hold_hours": json.RawMessage(`0`), "reminder_hours": json.RawMessage(`12`)},
		siteAdmin, time.Now())
	var invalid *apperr.Error
	require.ErrorAs(t, err, &invalid)
	require.Equal(t, apperr.InvalidRequest, invalid.Code)
	require.Equal(t, []apperr.FieldError{{Field: "/pendingHoldHours", Message: "must be from 1 to 168"}}, invalid.Fields)

	got, err := settings.Load(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, 24, got.ReminderHours, "a refused patch saves nothing")
}

func TestUpdateStoresTheCheckedValues(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	keep(t, q)
	got, err := settings.Update(context.Background(), q, settings.Patch{
		"contact_email":     json.RawMessage(`"Hello@VetMiMi.example"`),
		"slot_step_minutes": json.RawMessage(`15`),
	}, siteAdmin, time.Now())
	require.NoError(t, err)
	require.Equal(t, "hello@vetmimi.example", got.ContactEmail)
	require.Equal(t, 15, got.SlotStepMinutes)
}

func TestUpdateRefusesKeysOutsideTheRole(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	booking := auth.User{Roles: []string{"booking_admin"}}
	_, err := settings.Update(context.Background(), q,
		settings.Patch{"reminder_hours": json.RawMessage(`12`), "timezone": json.RawMessage(`"UTC"`)},
		booking, time.Now())
	var refused *apperr.Error
	require.ErrorAs(t, err, &refused)
	require.Equal(t, apperr.Forbidden, refused.Code)

	_, err = settings.Update(context.Background(), q, settings.Patch{}, siteAdmin, time.Now())
	require.ErrorAs(t, err, &refused)
	require.Equal(t, apperr.InvalidRequest, refused.Code)
}

func TestConcurrentPatchesOfDifferentKeysBothLand(t *testing.T) {
	q := db.New(pgtest.Pool(t))
	keep(t, q)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, patch := range []settings.Patch{
		{"reminder_hours": json.RawMessage(`6`)},
		{"retention_months": json.RawMessage(`36`)},
	} {
		wg.Go(func() {
			_, errs[i] = settings.Update(context.Background(), q, patch, siteAdmin, time.Now())
		})
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	got, err := settings.Load(context.Background(), q)
	require.NoError(t, err)
	require.Equal(t, 6, got.ReminderHours)
	require.Equal(t, 36, got.RetentionMonths)
}

// The timezone is stored as a name, so daylight saving follows the date.
func TestLocationFollowsDaylightSaving(t *testing.T) {
	loc, err := defaults.Location()
	require.NoError(t, err)
	summer, _ := time.Date(2026, 10, 4, 12, 0, 0, 0, loc).Zone()
	winter, _ := time.Date(2026, 4, 5, 12, 0, 0, 0, loc).Zone()
	require.Equal(t, "AEDT", summer)
	require.Equal(t, "AEST", winter)
}

func TestKeyNames(t *testing.T) {
	require.Equal(t, "late_cancellation_first_waived", settings.Key("lateCancellationFirstWaived"))
	require.Equal(t, "lateCancellationFirstWaived", settings.JSONName("late_cancellation_first_waived"))
}
