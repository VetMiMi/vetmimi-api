// Package settings reads and changes the settings table: the business rules
// Daw Mi may change without a deploy. Other packages read them through Load.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // so LoadLocation never depends on the host's files

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
)

// Group is who may change a key: booking keys need booking_admin, site keys
// site_admin. Every role may read the site keys; only booking and site
// administrators read the booking keys.
type Group string

// Localized is text per locale; en is required.
type Localized struct {
	En string `json:"en" binding:"max=20000"`
	My string `json:"my,omitempty" binding:"max=20000"`
}

// Settings holds every key; the JSON names are the table's keys.
type Settings struct {
	Timezone                    string    `json:"timezone"`
	BookingMode                 string    `json:"booking_mode"`
	PublicBookingEnabled        bool      `json:"public_booking_enabled"`
	PendingHoldHours            int       `json:"pending_hold_hours"`
	ReminderHours               int       `json:"reminder_hours"`
	MinNoticeHours              int       `json:"min_notice_hours"`
	MaxAdvanceDays              int       `json:"max_advance_days"`
	SlotStepMinutes             int       `json:"slot_step_minutes"`
	CancellationNoticeHours     int       `json:"cancellation_notice_hours"`
	LateCancellationFeePercent  int       `json:"late_cancellation_fee_percent"`
	LateCancellationFirstWaived bool      `json:"late_cancellation_first_waived"`
	NoShowFeePercent            int       `json:"no_show_fee_percent"`
	MeetingLinkMode             string    `json:"meeting_link_mode"`
	PaymentMethods              []string  `json:"payment_methods"`
	InvoiceTiming               string    `json:"invoice_timing"`
	RetentionMonths             int       `json:"retention_months"`
	ContactEmail                string    `json:"contact_email"`
	ResponseTime                Localized `json:"response_time"`

	updatedAt map[string]time.Time
}

// Patch is the keys to change, by table key, each with its new JSON value.
type Patch map[string]json.RawMessage

const (
	Booking Group = "booking"
	Site    Group = "site"
)

// TaskRescheduleReminders is run by comms, which imports this package.
const TaskRescheduleReminders = "comms:reschedule-reminders"

// Location is the practice timezone, stored as a name so daylight saving
// follows the date.
func (s Settings) Location() (*time.Location, error) {
	return time.LoadLocation(s.Timezone)
}

// CanRead reports whether a user holding roles may read the keys of group.
func CanRead(roles []string, group Group) bool {
	return group == Site || auth.HasRole(roles, auth.BookingAdmin)
}

// UpdatedAt is the newest change among the keys a user holding roles may
// read.
func (s Settings) UpdatedAt(roles []string) time.Time {
	var newest time.Time
	for _, setting := range keys {
		changed := s.updatedAt[setting.name]
		if CanRead(roles, setting.group) && changed.After(newest) {
			newest = changed
		}
	}
	return newest
}

func Load(ctx context.Context, q db.Querier) (Settings, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	values := make(map[string]json.RawMessage, len(rows))
	loaded := Settings{updatedAt: make(map[string]time.Time, len(rows))}
	for _, row := range rows {
		values[row.Key] = row.Value
		loaded.updatedAt[row.Key] = row.UpdatedAt
	}
	body, err := json.Marshal(values)
	if err != nil {
		return Settings{}, err
	}
	err = json.Unmarshal(body, &loaded)
	if err != nil {
		return Settings{}, fmt.Errorf("settings: decode the stored values: %w", err)
	}
	return loaded, nil
}

// RescheduleTasks moves queued reminders when patch changes reminder_hours.
// The task reads the stored setting, so a repeat is harmless.
func RescheduleTasks(patch Patch, now time.Time) []queue.Task {
	_, changed := patch["reminder_hours"]
	if !changed {
		return nil
	}
	task := queue.Task{
		Type: TaskRescheduleReminders,
		ID:   "reminders:" + strconv.FormatInt(now.UnixNano(), 10),
	}
	return []queue.Task{task}
}

// Update applies patch for user by and returns the settings after it. The
// patch lands whole or not at all: a key by may not change answers forbidden,
// an unknown key or a value out of range invalid_request.
func Update(ctx context.Context, q db.Querier, patch Patch, by auth.User, now time.Time) (Settings, error) {
	if len(patch) == 0 {
		return Settings{}, apperr.Invalid("Change at least one setting.")
	}
	names := slices.Sorted(maps.Keys(patch))
	err := checkKeys(names, by)
	if err != nil {
		return Settings{}, err
	}
	decoded, err := decodePatch(patch)
	if err != nil {
		return Settings{}, err
	}
	err = checkValues(names, &decoded)
	if err != nil {
		return Settings{}, err
	}
	body, err := changedValues(decoded, names)
	if err != nil {
		return Settings{}, err
	}
	err = q.UpdateSettings(ctx, db.UpdateSettingsParams{
		UpdatedBy: by.ID,
		Now:       now,
		Patch:     body,
	})
	if err != nil {
		return Settings{}, err
	}
	return Load(ctx, q)
}

// checkKeys refuses a key by may not change, then any unknown keys.
func checkKeys(names []string, by auth.User) error {
	var unknown []apperr.FieldError
	for _, name := range names {
		setting, ok := keyNamed(name)
		if !ok {
			unknown = append(unknown, apperr.FieldError{Field: "/" + JSONName(name), Message: "is not a setting"})
			continue
		}
		if !mayChange(by.Roles, setting.group) {
			return apperr.New(apperr.Forbidden, "Your role does not allow this.")
		}
	}
	if len(unknown) > 0 {
		return apperr.Invalid("Unknown settings.", unknown...)
	}
	return nil
}

// decodePatch decodes patch into Settings and stores contact_email lower-case.
func decodePatch(patch Patch) (Settings, error) {
	body, err := json.Marshal(patch)
	if err != nil {
		return Settings{}, err
	}
	var decoded Settings
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	err = decoder.Decode(&decoded)
	if err != nil {
		return Settings{}, apperr.Invalid("Each setting must have its documented type.")
	}
	decoded.ContactEmail = strings.ToLower(decoded.ContactEmail)
	return decoded, nil
}

func checkValues(names []string, decoded *Settings) error {
	var wrong []apperr.FieldError
	for _, name := range names {
		setting, _ := keyNamed(name) // checkKeys has refused unknown names
		if !setting.valid(decoded) {
			wrong = append(wrong, apperr.FieldError{Field: "/" + JSONName(name), Message: setting.rule})
		}
	}
	if len(wrong) > 0 {
		return apperr.Invalid("Settings out of range.", wrong...)
	}
	return nil
}

// changedValues re-encodes the named keys from decoded, so what is stored is
// the checked, normalised value.
func changedValues(decoded Settings, names []string) ([]byte, error) {
	normal, err := json.Marshal(decoded)
	if err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	err = json.Unmarshal(normal, &all)
	if err != nil {
		return nil, err
	}
	changed := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		changed[name] = all[name]
	}
	return json.Marshal(changed)
}
