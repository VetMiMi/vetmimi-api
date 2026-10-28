package settings

import (
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
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

// patchInput checks the keys a patch sets; omitnil checks a value only when it is sent.
type patchInput struct {
	Timezone                    *string    `json:"timezone"`
	BookingMode                 *string    `json:"bookingMode" binding:"omitnil,oneof=request_approval instant"`
	PublicBookingEnabled        *bool      `json:"publicBookingEnabled"`
	PendingHoldHours            *int       `json:"pendingHoldHours" binding:"omitnil,min=1,max=168"`
	ReminderHours               *int       `json:"reminderHours" binding:"omitnil,min=1,max=168"`
	MinNoticeHours              *int       `json:"minNoticeHours" binding:"omitnil,min=0,max=720"`
	MaxAdvanceDays              *int       `json:"maxAdvanceDays" binding:"omitnil,min=1,max=365"`
	SlotStepMinutes             *int       `json:"slotStepMinutes" binding:"omitnil,min=5,max=120"`
	CancellationNoticeHours     *int       `json:"cancellationNoticeHours" binding:"omitnil,min=0,max=168"`
	LateCancellationFeePercent  *int       `json:"lateCancellationFeePercent" binding:"omitnil,min=0,max=100"`
	LateCancellationFirstWaived *bool      `json:"lateCancellationFirstWaived"`
	NoShowFeePercent            *int       `json:"noShowFeePercent" binding:"omitnil,min=0,max=100"`
	MeetingLinkMode             *string    `json:"meetingLinkMode" binding:"omitnil,oneof=vetmimi_room manual_link"`
	PaymentMethods              []string   `json:"paymentMethods" binding:"omitnil,min=1,unique,dive,oneof=bank_transfer card"`
	InvoiceTiming               *string    `json:"invoiceTiming" binding:"omitnil,oneof=after_session"`
	RetentionMonths             *int       `json:"retentionMonths" binding:"omitnil,min=6,max=120"`
	ContactEmail                *string    `json:"contactEmail" binding:"omitnil,email"`
	ResponseTime                *Localized `json:"responseTime"`
}

// updateSettings applies all of the patch or none of it.
func (h *Handler) updateSettings(c *gin.Context) {
	var in patchInput
	var sent map[string]json.RawMessage
	if !web.BindJSON(c, &in) || !web.BindJSON(c, &sent) {
		return
	}
	patch, err := patchOf(sent)
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
	if h.Queue != nil {
		h.Queue.Enqueue(ctx, RescheduleTasks(patch, now)...)
	}
	slog.InfoContext(ctx, "settings_changed", "request_id", web.RequestID(ctx),
		"user_id", session.User.ID.String(), "keys", slices.Sorted(maps.Keys(patch)))
	c.JSON(http.StatusOK, settingsView(updated, session.User.Roles))
}

// patchOf keys each value sent by its table key. A null is refused: no setting can be cleared.
func patchOf(sent map[string]json.RawMessage) (Patch, error) {
	patch := make(Patch, len(sent))
	var nulls []apperr.FieldError
	for _, name := range slices.Sorted(maps.Keys(sent)) {
		if string(sent[name]) == "null" {
			nulls = append(nulls, apperr.FieldError{Field: "/" + name, Message: "must not be null"})
			continue
		}
		patch[Key(name)] = sent[name]
	}
	if nulls != nil {
		return nil, apperr.Invalid("The request is not valid.", nulls...)
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
