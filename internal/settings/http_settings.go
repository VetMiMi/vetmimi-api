package settings

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// settingsResponse leaves out the booking keys for a role that may not read them.
type settingsResponse struct {
	Timezone        string    `json:"timezone"`
	RetentionMonths int       `json:"retentionMonths"`
	ContactEmail    string    `json:"contactEmail"`
	ResponseTime    Localized `json:"responseTime"`
	UpdatedAt       time.Time `json:"updatedAt"`
	*bookingSettings
}

type bookingSettings struct {
	BookingMode                 string   `json:"bookingMode"`
	PublicBookingEnabled        bool     `json:"publicBookingEnabled"`
	PendingHoldHours            int      `json:"pendingHoldHours"`
	ReminderHours               int      `json:"reminderHours"`
	MinNoticeHours              int      `json:"minNoticeHours"`
	MaxAdvanceDays              int      `json:"maxAdvanceDays"`
	SlotStepMinutes             int      `json:"slotStepMinutes"`
	CancellationNoticeHours     int      `json:"cancellationNoticeHours"`
	LateCancellationFeePercent  int      `json:"lateCancellationFeePercent"`
	LateCancellationFirstWaived bool     `json:"lateCancellationFirstWaived"`
	NoShowFeePercent            int      `json:"noShowFeePercent"`
	MeetingLinkMode             string   `json:"meetingLinkMode"`
	PaymentMethods              []string `json:"paymentMethods"`
	InvoiceTiming               string   `json:"invoiceTiming"`
}

func (h *Handler) getSettings(c *gin.Context) {
	session, _ := auth.FromContext(c.Request.Context())
	current, err := Load(c.Request.Context(), db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, settingsView(current, session.User.Roles))
}

// patchInput holds the keys a patch sets; omitnil checks a value only when it is sent.
type patchInput struct {
	Timezone                    *string    `json:"timezone,omitempty"`
	BookingMode                 *string    `json:"bookingMode,omitempty" binding:"omitnil,oneof=request_approval instant"`
	PublicBookingEnabled        *bool      `json:"publicBookingEnabled,omitempty"`
	PendingHoldHours            *int       `json:"pendingHoldHours,omitempty" binding:"omitnil,min=1,max=168"`
	ReminderHours               *int       `json:"reminderHours,omitempty" binding:"omitnil,min=1,max=168"`
	MinNoticeHours              *int       `json:"minNoticeHours,omitempty" binding:"omitnil,min=0,max=720"`
	MaxAdvanceDays              *int       `json:"maxAdvanceDays,omitempty" binding:"omitnil,min=1,max=365"`
	SlotStepMinutes             *int       `json:"slotStepMinutes,omitempty" binding:"omitnil,min=5,max=120"`
	CancellationNoticeHours     *int       `json:"cancellationNoticeHours,omitempty" binding:"omitnil,min=0,max=168"`
	LateCancellationFeePercent  *int       `json:"lateCancellationFeePercent,omitempty" binding:"omitnil,min=0,max=100"`
	LateCancellationFirstWaived *bool      `json:"lateCancellationFirstWaived,omitempty"`
	NoShowFeePercent            *int       `json:"noShowFeePercent,omitempty" binding:"omitnil,min=0,max=100"`
	MeetingLinkMode             *string    `json:"meetingLinkMode,omitempty" binding:"omitnil,oneof=vetmimi_room manual_link"`
	PaymentMethods              []string   `json:"paymentMethods,omitempty" binding:"omitnil,min=1,unique,dive,oneof=bank_transfer card"`
	InvoiceTiming               *string    `json:"invoiceTiming,omitempty" binding:"omitnil,oneof=after_session"`
	RetentionMonths             *int       `json:"retentionMonths,omitempty" binding:"omitnil,min=6,max=120"`
	ContactEmail                *string    `json:"contactEmail,omitempty" binding:"omitnil,email"`
	ResponseTime                *Localized `json:"responseTime,omitempty"`
}

// updateSettings applies all of the patch or none of it.
func (h *Handler) updateSettings(c *gin.Context) {
	var in patchInput
	if !web.BindJSON(c, &in) {
		return
	}
	patch, err := in.patch()
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()
	session, _ := auth.FromContext(ctx)
	now := h.Now()
	updated, err := Update(ctx, db.New(h.Pool), patch, session.User, now)
	if err != nil {
		web.Error(c, err)
		return
	}
	if h.Queue != nil && h.Tasks != nil {
		h.Queue.Enqueue(ctx, h.Tasks(patch, now)...)
	}
	slog.InfoContext(ctx, "settings_changed", "request_id", web.RequestID(ctx),
		"user_id", session.User.ID.String(), "keys", slices.Sorted(maps.Keys(patch)))
	c.JSON(http.StatusOK, settingsView(updated, session.User.Roles))
}

// patch keys each value the body sets by its table key.
func (in patchInput) patch() (Patch, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, err
	}
	patch := make(Patch, len(fields))
	for name, value := range fields {
		patch[Key(name)] = value
	}
	return patch, nil
}

func settingsView(s Settings, roles []string) settingsResponse {
	v := settingsResponse{
		Timezone:        s.Timezone,
		RetentionMonths: s.RetentionMonths,
		ContactEmail:    s.ContactEmail,
		ResponseTime:    s.ResponseTime,
		UpdatedAt:       s.UpdatedAt(roles).UTC(),
	}
	if !CanRead(roles, Booking) {
		return v
	}
	v.bookingSettings = &bookingSettings{
		BookingMode:                 s.BookingMode,
		PublicBookingEnabled:        s.PublicBookingEnabled,
		PendingHoldHours:            s.PendingHoldHours,
		ReminderHours:               s.ReminderHours,
		MinNoticeHours:              s.MinNoticeHours,
		MaxAdvanceDays:              s.MaxAdvanceDays,
		SlotStepMinutes:             s.SlotStepMinutes,
		CancellationNoticeHours:     s.CancellationNoticeHours,
		LateCancellationFeePercent:  s.LateCancellationFeePercent,
		LateCancellationFirstWaived: s.LateCancellationFirstWaived,
		NoShowFeePercent:            s.NoShowFeePercent,
		MeetingLinkMode:             s.MeetingLinkMode,
		PaymentMethods:              s.PaymentMethods,
		InvoiceTiming:               s.InvoiceTiming,
	}
	if v.PaymentMethods == nil {
		v.PaymentMethods = []string{}
	}
	return v
}
