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

// updateSettingsBody checks the keys a patch sets; omitnil checks a value only when it is sent.
type updateSettingsBody struct {
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

// settingsResponse leaves out the booking keys for a role that may not read them.
type settingsResponse struct {
	Timezone        string    `json:"timezone"`
	RetentionMonths int       `json:"retentionMonths"`
	ContactEmail    string    `json:"contactEmail"`
	ResponseTime    Localized `json:"responseTime"`
	UpdatedAt       time.Time `json:"updatedAt"`
	*bookingSettingsResponse
}

type bookingSettingsResponse struct {
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
	ctx := c.Request.Context()

	session, _ := auth.FromContext(ctx)
	current, err := Load(ctx, db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newSettingsResponse(current, session.User.Roles))
}

// updateSettings applies all of the patch or none of it.
func (h *Handler) updateSettings(c *gin.Context) {
	var body updateSettingsBody
	if !web.BindJSON(c, &body) {
		return
	}
	var sent map[string]json.RawMessage
	if !web.BindJSON(c, &sent) {
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

	h.Queue.Enqueue(ctx, RescheduleTasks(patch, now)...)
	slog.InfoContext(ctx, "settings_changed",
		"request_id", web.RequestID(ctx),
		"user_id", session.User.ID.String(),
		"keys", slices.Sorted(maps.Keys(patch)),
	)

	c.JSON(http.StatusOK, newSettingsResponse(updated, session.User.Roles))
}

func newSettingsResponse(current Settings, roles []string) settingsResponse {
	response := settingsResponse{
		Timezone:        current.Timezone,
		RetentionMonths: current.RetentionMonths,
		ContactEmail:    current.ContactEmail,
		ResponseTime:    current.ResponseTime,
		UpdatedAt:       current.UpdatedAt(roles).UTC(),
	}
	if !CanRead(roles, Booking) {
		return response
	}
	response.bookingSettingsResponse = &bookingSettingsResponse{
		BookingMode:                 current.BookingMode,
		PublicBookingEnabled:        current.PublicBookingEnabled,
		PendingHoldHours:            current.PendingHoldHours,
		ReminderHours:               current.ReminderHours,
		MinNoticeHours:              current.MinNoticeHours,
		MaxAdvanceDays:              current.MaxAdvanceDays,
		SlotStepMinutes:             current.SlotStepMinutes,
		CancellationNoticeHours:     current.CancellationNoticeHours,
		LateCancellationFeePercent:  current.LateCancellationFeePercent,
		LateCancellationFirstWaived: current.LateCancellationFirstWaived,
		NoShowFeePercent:            current.NoShowFeePercent,
		MeetingLinkMode:             current.MeetingLinkMode,
		PaymentMethods:              current.PaymentMethods,
		InvoiceTiming:               current.InvoiceTiming,
	}
	if response.PaymentMethods == nil {
		response.PaymentMethods = []string{}
	}
	return response
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
