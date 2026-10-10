package httpapi

import (
	"context"
	"encoding/json"
	"maps"
	"slices"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
	"github.com/VetMiMi/vetmimi-api/internal/settings"
)

// GetSettings gives a content editor the site keys only.
func (s *server) GetSettings(ctx context.Context, _ gen.GetSettingsRequestObject) (gen.GetSettingsResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	current, err := settings.Load(ctx, db.New(s.Pool))
	if err != nil {
		return nil, err
	}
	return gen.GetSettings200JSONResponse(settingsView(current, session.User.Roles)), nil
}

// UpdateSettings applies all of the patch or none of it.
func (s *server) UpdateSettings(ctx context.Context, req gen.UpdateSettingsRequestObject) (gen.UpdateSettingsResponseObject, error) {
	session, _ := auth.FromContext(ctx)
	patch, err := settingsPatch(*req.Body)
	if err != nil {
		return nil, err
	}
	now := s.Now()
	updated, err := settings.Update(ctx, db.New(s.Pool), patch, session.User, now)
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, comms.ReminderRescheduleTasks(patch, now)...)
	s.Log.InfoContext(ctx, "settings_changed", "request_id", RequestID(ctx),
		"user_id", session.User.ID.String(), "keys", slices.Sorted(maps.Keys(patch)))
	return gen.UpdateSettings200JSONResponse(settingsView(updated, session.User.Roles)), nil
}

// settingsPatch keys each field the body sets by its table key. The validator
// has already refused unknown fields.
func settingsPatch(body gen.SettingsPatch) (settings.Patch, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	patch := make(settings.Patch, len(fields))
	for name, value := range fields {
		patch[settings.Key(name)] = value
	}
	return patch, nil
}

func settingsView(s settings.Settings, roles []string) gen.Settings {
	v := gen.Settings{
		Timezone:        gen.Timezone(s.Timezone),
		RetentionMonths: s.RetentionMonths,
		ContactEmail:    openapi_types.Email(s.ContactEmail),
		ResponseTime:    localizedText(s.ResponseTime),
		UpdatedAt:       new(s.UpdatedAt(roles).UTC()),
	}
	if !settings.CanRead(roles, settings.Booking) {
		return v
	}
	methods := make([]gen.SettingsPaymentMethods, len(s.PaymentMethods))
	for i, m := range s.PaymentMethods {
		methods[i] = gen.SettingsPaymentMethods(m)
	}
	v.BookingMode = new(gen.SettingsBookingMode(s.BookingMode))
	v.PublicBookingEnabled = new(s.PublicBookingEnabled)
	v.PendingHoldHours = new(s.PendingHoldHours)
	v.ReminderHours = new(s.ReminderHours)
	v.MinNoticeHours = new(s.MinNoticeHours)
	v.MaxAdvanceDays = new(s.MaxAdvanceDays)
	v.SlotStepMinutes = new(s.SlotStepMinutes)
	v.CancellationNoticeHours = new(s.CancellationNoticeHours)
	v.LateCancellationFeePercent = new(s.LateCancellationFeePercent)
	v.LateCancellationFirstWaived = new(s.LateCancellationFirstWaived)
	v.NoShowFeePercent = new(s.NoShowFeePercent)
	v.MeetingLinkMode = new(gen.SettingsMeetingLinkMode(s.MeetingLinkMode))
	v.PaymentMethods = &methods
	v.InvoiceTiming = new(gen.SettingsInvoiceTiming(s.InvoiceTiming))
	return v
}

func localizedText(l settings.Localized) gen.LocalizedText {
	t := gen.LocalizedText{En: new(l.En)}
	if l.My != "" {
		t.My = new(l.My)
	}
	return t
}
