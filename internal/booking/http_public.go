package booking

import (
	"cmp"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type localeQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
}

func (h *Handler) listPublicServices(c *gin.Context) {
	var q localeQuery
	if !web.BindQuery(c, &q) {
		return
	}
	list, err := ListPublicServices(c.Request.Context(), db.New(h.Pool), cmp.Or(q.Locale, "en"))
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, list)
}

type availabilityQuery struct {
	Service string    `form:"service" binding:"required,slug"`
	From    time.Time `form:"from" binding:"required" time_format:"2006-01-02" time_utc:"1"`
	To      time.Time `form:"to" binding:"required" time_format:"2006-01-02" time_utc:"1"`
}

type availabilityResponse struct {
	Service  string        `json:"service"`
	Timezone string        `json:"timezone"`
	From     string        `json:"from"`
	To       string        `json:"to"`
	Days     []slotDayJSON `json:"days"`
}

type slotDayJSON struct {
	Date  string     `json:"date"`
	Slots []slotJSON `json:"slots"`
}

// publicAvailability answers times only, never appointments or why time is blocked.
func (h *Handler) publicAvailability(c *gin.Context) {
	var q availabilityQuery
	if !web.BindQuery(c, &q) {
		return
	}
	a, err := PublicAvailability(c.Request.Context(), db.New(h.Pool), q.Service, q.From, q.To, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, availabilityResponse{
		Service:  q.Service,
		Timezone: a.Timezone,
		From:     a.First.Format(time.DateOnly),
		To:       a.Last.Format(time.DateOnly),
		Days:     slotDaysJSON(a.Days),
	})
}

func slotDaysJSON(days []SlotDay) []slotDayJSON {
	out := make([]slotDayJSON, len(days))
	for i, d := range days {
		out[i] = slotDayJSON{Date: d.Date.Format(time.DateOnly), Slots: make([]slotJSON, len(d.Slots))}
		for j, s := range d.Slots {
			out[i].Slots[j] = slotJSON{StartsAt: s.Start.UTC(), EndsAt: s.End.UTC()}
		}
	}
	return out
}

type visitorInput struct {
	Name  string `json:"name" binding:"required,max=120"`
	Email string `json:"email" binding:"required,email,max=254"`
	Phone string `json:"phone,omitempty" binding:"omitempty,phone"`
	Note  string `json:"note,omitempty" binding:"max=500"`
}

type requestInput struct {
	Service             string       `json:"service" binding:"required,slug"`
	StartsAt            time.Time    `json:"startsAt" binding:"required"`
	Format              string       `json:"format" binding:"required,oneof=online in_person"`
	Locale              string       `json:"locale" binding:"required,oneof=en my"`
	Visitor             visitorInput `json:"visitor"`
	PrivacyAcknowledged bool         `json:"privacyAcknowledged"`
	PolicyAcknowledged  bool         `json:"policyAcknowledged"`
}

// requestAppointment stores a visitor's request, then queues its emails and hold expiry.
// The receipt never carries the id or the management token.
func (h *Handler) requestAppointment(c *gin.Context) {
	key, err := uuid.Parse(c.GetHeader("Idempotency-Key"))
	if err != nil {
		web.Error(c, apperr.Invalid("Idempotency-Key must be a UUID.",
			apperr.FieldError{Field: "Idempotency-Key", Message: "must be a valid uuid"}))
		return
	}
	var in requestInput
	if !web.BindJSON(c, &in) {
		return
	}
	// The key's hash covers the body as decoded, so a retry that differs only in spacing is the same request.
	body, err := json.Marshal(in)
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()
	res, err := RequestAppointment(ctx, h.Pool, h.Secret, Request{
		IdempotencyKey:      key.String(),
		Body:                body,
		Service:             in.Service,
		StartsAt:            in.StartsAt,
		Format:              in.Format,
		Locale:              in.Locale,
		VisitorName:         in.Visitor.Name,
		VisitorEmail:        in.Visitor.Email,
		VisitorPhone:        in.Visitor.Phone,
		VisitorNote:         in.Visitor.Note,
		PrivacyAcknowledged: in.PrivacyAcknowledged,
		PolicyAcknowledged:  in.PolicyAcknowledged,
	}, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterCommit(ctx, Changed{Tasks: res.Tasks})
	slog.InfoContext(ctx, "appointment requested", "request_id", web.RequestID(ctx),
		"appointment_id", res.AppointmentID.String(), "replayed", res.Replayed)
	if res.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	c.JSON(http.StatusCreated, res.Receipt)
}

// getManaged answers the same 404 for an unknown and an expired link.
func (h *Handler) getManaged(c *gin.Context) {
	m, err := GetManaged(c.Request.Context(), db.New(h.Pool), c.Param("token"), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, m)
}

type cancelInput struct {
	Message string `json:"message" binding:"max=500"`
}

func (h *Handler) cancelManaged(c *gin.Context) {
	var in cancelInput
	if c.Request.ContentLength != 0 && !web.BindJSON(c, &in) {
		return
	}
	changed, err := CancelByClient(c.Request.Context(), h.Pool, c.Param("token"), in.Message, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterManagedChange(c, "appointment_cancelled_by_client", changed, http.StatusOK)
}

type rescheduleRequestInput struct {
	PreferredTimes []time.Time `json:"preferredTimes" binding:"max=3"`
	Message        string      `json:"message" binding:"max=500"`
}

// requestManagedReschedule tells Daw Mi; the appointment keeps its time until she moves it.
func (h *Handler) requestManagedReschedule(c *gin.Context) {
	var in rescheduleRequestInput
	if !web.BindJSON(c, &in) {
		return
	}
	changed, err := RequestReschedule(c.Request.Context(), h.Pool, c.Param("token"), in.PreferredTimes, in.Message, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterManagedChange(c, "reschedule_requested", changed, http.StatusAccepted)
}

// afterManagedChange logs the event only: the token is as good as a password.
func (h *Handler) afterManagedChange(c *gin.Context, event string, changed Changed, status int) {
	ctx := c.Request.Context()
	h.afterCommit(ctx, changed)
	slog.InfoContext(ctx, event, "request_id", web.RequestID(ctx))
	m, err := GetManaged(ctx, db.New(h.Pool), c.Param("token"), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(status, m)
}
