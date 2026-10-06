// Package settings reads and changes the settings table: the business rules
// Daw Mi may change without a deploy (docs/data-model.md, "Settings keys").
// Booking, comms and video code read settings only through Load.
package settings

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/mail"
	"slices"
	"strings"
	"time"
	// The timezone setting is checked and applied with time.LoadLocation;
	// embedding the database means that never depends on the host's files.
	_ "time/tzdata"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// Group is who may change a key: booking keys need booking_admin, site keys
// site_admin. Every role may read the site keys; only booking and site
// administrators read the booking keys.
type Group string

const (
	Booking Group = "booking"
	Site    Group = "site"
)

// Localized is text per locale; en is required.
type Localized struct {
	En string `json:"en"`
	My string `json:"my,omitempty"`
}

// Settings holds every key, decoded from its row. The JSON names are the
// table's keys.
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

// Location is the practice timezone. It is stored as a name, never an
// offset, so daylight saving follows the date.
func (s Settings) Location() (*time.Location, error) {
	return time.LoadLocation(s.Timezone)
}

// CanRead reports whether a user holding roles may read the keys of g.
func CanRead(roles []string, g Group) bool {
	return g == Site || auth.HasRole(roles, auth.BookingAdmin)
}

// UpdatedAt is the newest change among the keys a user holding roles may
// read.
func (s Settings) UpdatedAt(roles []string) time.Time {
	var newest time.Time
	for _, k := range keys {
		if t := s.updatedAt[k.name]; CanRead(roles, k.group) && t.After(newest) {
			newest = t
		}
	}
	return newest
}

// Load reads every setting.
func Load(ctx context.Context, q db.Querier) (Settings, error) {
	rows, err := q.ListSettings(ctx)
	if err != nil {
		return Settings{}, err
	}
	values := make(map[string]json.RawMessage, len(rows))
	s := Settings{updatedAt: make(map[string]time.Time, len(rows))}
	for _, r := range rows {
		values[r.Key] = r.Value
		s.updatedAt[r.Key] = r.UpdatedAt
	}
	body, err := json.Marshal(values)
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal(body, &s); err != nil {
		return Settings{}, fmt.Errorf("settings: decode the stored values: %w", err)
	}
	return s, nil
}

// Patch is the keys to change, by table key, each with its new JSON value.
type Patch map[string]json.RawMessage

// Update applies patch for user by, at now, and returns the settings after
// it. The patch lands whole or not at all: a key by may not change answers
// forbidden, and an unknown key or a value out of range invalid_request,
// with nothing saved. contact_email is stored lower-case.
func Update(ctx context.Context, q db.Querier, patch Patch, by auth.User, now time.Time) (Settings, error) {
	if len(patch) == 0 {
		return Settings{}, apperr.Invalid("Change at least one setting.")
	}
	names := slices.Sorted(maps.Keys(patch))
	var unknown []apperr.FieldError
	for _, name := range names {
		k, ok := keyNamed(name)
		if !ok {
			unknown = append(unknown, apperr.FieldError{Field: "/" + JSONName(name), Message: "is not a setting"})
			continue
		}
		if !mayChange(by.Roles, k.group) {
			return Settings{}, apperr.New(apperr.Forbidden, "Your role does not allow this.")
		}
	}
	if len(unknown) > 0 {
		return Settings{}, apperr.Invalid("Unknown settings.", unknown...)
	}

	body, err := json.Marshal(patch)
	if err != nil {
		return Settings{}, err
	}
	var s Settings
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return Settings{}, apperr.Invalid("Each setting must have its documented type.")
	}
	s.ContactEmail = strings.ToLower(s.ContactEmail)

	var wrong []apperr.FieldError
	for _, name := range names {
		if k, _ := keyNamed(name); !k.valid(&s) {
			wrong = append(wrong, apperr.FieldError{Field: "/" + JSONName(name), Message: k.rule})
		}
	}
	if len(wrong) > 0 {
		return Settings{}, apperr.Invalid("Settings out of range.", wrong...)
	}

	// Re-encode from s, so what is stored is the checked, normalised value.
	normal, err := json.Marshal(s)
	if err != nil {
		return Settings{}, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(normal, &all); err != nil {
		return Settings{}, err
	}
	changed := make(map[string]json.RawMessage, len(names))
	for _, name := range names {
		changed[name] = all[name]
	}
	if body, err = json.Marshal(changed); err != nil {
		return Settings{}, err
	}
	if err := q.UpdateSettings(ctx, db.UpdateSettingsParams{UpdatedBy: by.ID, Now: now, Patch: body}); err != nil {
		return Settings{}, err
	}
	return Load(ctx, q)
}

func mayChange(roles []string, g Group) bool {
	if g == Site {
		return auth.HasRole(roles, auth.SiteAdmin)
	}
	return auth.HasRole(roles, auth.BookingAdmin)
}

// Key is the table key for the API's camelCase name of it.
func Key(jsonName string) string {
	var b strings.Builder
	for _, r := range jsonName {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// JSONName is the API's camelCase name for a table key.
func JSONName(key string) string {
	parts := strings.Split(key, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// key is one row of docs/data-model.md, "Settings keys": its group and the
// rule its value must meet, with the same bounds as openapi.yaml, so a
// caller other than the HTTP API cannot store what the API would refuse.
type key struct {
	name  string
	group Group
	valid func(*Settings) bool
	rule  string
}

var keys = []key{
	{"timezone", Site, func(s *Settings) bool { return isTimezone(s.Timezone) }, "must be an IANA timezone name"},
	oneOf("booking_mode", Booking, func(s *Settings) string { return s.BookingMode }, "request_approval", "instant"),
	{"public_booking_enabled", Booking, func(*Settings) bool { return true }, ""},
	between("pending_hold_hours", Booking, func(s *Settings) int { return s.PendingHoldHours }, 1, 168),
	between("reminder_hours", Booking, func(s *Settings) int { return s.ReminderHours }, 1, 168),
	between("min_notice_hours", Booking, func(s *Settings) int { return s.MinNoticeHours }, 0, 720),
	between("max_advance_days", Booking, func(s *Settings) int { return s.MaxAdvanceDays }, 1, 365),
	between("slot_step_minutes", Booking, func(s *Settings) int { return s.SlotStepMinutes }, 5, 120),
	between("cancellation_notice_hours", Booking, func(s *Settings) int { return s.CancellationNoticeHours }, 0, 168),
	between("late_cancellation_fee_percent", Booking, func(s *Settings) int { return s.LateCancellationFeePercent }, 0, 100),
	{"late_cancellation_first_waived", Booking, func(*Settings) bool { return true }, ""},
	between("no_show_fee_percent", Booking, func(s *Settings) int { return s.NoShowFeePercent }, 0, 100),
	oneOf("meeting_link_mode", Booking, func(s *Settings) string { return s.MeetingLinkMode }, "vetmimi_room", "manual_link"),
	{"payment_methods", Booking, func(s *Settings) bool { return isPaymentMethods(s.PaymentMethods) },
		"must list bank_transfer or card, each at most once"},
	oneOf("invoice_timing", Booking, func(s *Settings) string { return s.InvoiceTiming }, "after_session"),
	between("retention_months", Site, func(s *Settings) int { return s.RetentionMonths }, 6, 120),
	{"contact_email", Site, func(s *Settings) bool { return isEmail(s.ContactEmail) }, "must be an email address"},
	{"response_time", Site, func(s *Settings) bool { return s.ResponseTime.En != "" }, "needs en"},
}

func keyNamed(name string) (key, bool) {
	i := slices.IndexFunc(keys, func(k key) bool { return k.name == name })
	if i < 0 {
		return key{}, false
	}
	return keys[i], true
}

func between(name string, g Group, field func(*Settings) int, lo, hi int) key {
	return key{name, g, func(s *Settings) bool {
		v := field(s)
		return v >= lo && v <= hi
	}, fmt.Sprintf("must be from %d to %d", lo, hi)}
}

func oneOf(name string, g Group, field func(*Settings) string, allowed ...string) key {
	return key{name, g, func(s *Settings) bool {
		return slices.Contains(allowed, field(s))
	}, "must be one of " + strings.Join(allowed, ", ")}
}

// isTimezone refuses "" and "Local", which LoadLocation accepts as UTC and
// the host's zone.
func isTimezone(name string) bool {
	if name == "" || name == "Local" {
		return false
	}
	_, err := time.LoadLocation(name)
	return err == nil
}

func isPaymentMethods(methods []string) bool {
	if len(methods) == 0 {
		return false
	}
	seen := map[string]bool{}
	for _, m := range methods {
		if (m != "bank_transfer" && m != "card") || seen[m] {
			return false
		}
		seen[m] = true
	}
	return true
}

func isEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}
