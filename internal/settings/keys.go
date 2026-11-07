package settings

import (
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
)

// key is one settings key: who may change it and the rule its value must
// meet.
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

// Key is the table key for the API's camelCase name of it.
func Key(jsonName string) string {
	var builder strings.Builder
	for _, letter := range jsonName {
		if letter >= 'A' && letter <= 'Z' {
			builder.WriteByte('_')
			letter += 'a' - 'A'
		}
		builder.WriteRune(letter)
	}
	return builder.String()
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

func keyNamed(name string) (key, bool) {
	i := slices.IndexFunc(keys, func(setting key) bool { return setting.name == name })
	if i < 0 {
		return key{}, false
	}
	return keys[i], true
}

func mayChange(roles []string, group Group) bool {
	if group == Site {
		return auth.HasRole(roles, auth.SiteAdmin)
	}
	return auth.HasRole(roles, auth.BookingAdmin)
}

func between(name string, group Group, field func(*Settings) int, low, high int) key {
	return key{
		name:  name,
		group: group,
		valid: func(s *Settings) bool {
			value := field(s)
			return value >= low && value <= high
		},
		rule: fmt.Sprintf("must be from %d to %d", low, high),
	}
}

func oneOf(name string, group Group, field func(*Settings) string, allowed ...string) key {
	return key{
		name:  name,
		group: group,
		valid: func(s *Settings) bool {
			return slices.Contains(allowed, field(s))
		},
		rule: "must be one of " + strings.Join(allowed, ", "),
	}
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
	for _, method := range methods {
		if (method != "bank_transfer" && method != "card") || seen[method] {
			return false
		}
		seen[method] = true
	}
	return true
}

func isEmail(text string) bool {
	address, err := mail.ParseAddress(text)
	return err == nil && address.Address == text
}
