package booking

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type tokenPath struct {
	Token string `uri:"token" binding:"required"`
}

type listPublicServicesQuery struct {
	Locale string `form:"locale" binding:"omitempty,oneof=en my"`
}

type publicAvailabilityQuery struct {
	Service string    `form:"service" binding:"required,slug"`
	From    time.Time `form:"from" binding:"required" time_format:"2006-01-02" time_utc:"1"`
	To      time.Time `form:"to" binding:"required" time_format:"2006-01-02" time_utc:"1"`
}

// requestAppointmentBody's fields keep their order: the idempotency key's hash covers the body as re-encoded.
type requestAppointmentBody struct {
	Service             string      `json:"service" binding:"required,slug"`
	StartsAt            time.Time   `json:"startsAt" binding:"required"`
	Format              string      `json:"format" binding:"required,oneof=online in_person"`
	Locale              string      `json:"locale" binding:"required,oneof=en my"`
	Visitor             visitorBody `json:"visitor"`
	PrivacyAcknowledged bool        `json:"privacyAcknowledged"`
	PolicyAcknowledged  bool        `json:"policyAcknowledged"`
}

type visitorBody struct {
	Name  string `json:"name" binding:"required,max=120"`
	Email string `json:"email" binding:"required,email,max=254"`
	Phone string `json:"phone,omitempty" binding:"omitempty,phone"`
	Note  string `json:"note,omitempty" binding:"max=500"`
}

type cancelManagedBody struct {
	Message string `json:"message" binding:"max=500"`
}

type requestManagedRescheduleBody struct {
	PreferredTimes []time.Time `json:"preferredTimes" binding:"max=3"`
	Message        string      `json:"message" binding:"max=500"`
}

type availabilityResponse struct {
	Service  string            `json:"service"`
	Timezone string            `json:"timezone"`
	From     string            `json:"from"`
	To       string            `json:"to"`
	Days     []slotDayResponse `json:"days"`
}

type slotDayResponse struct {
	Date  string     `json:"date"`
	Slots []slotJSON `json:"slots"`
}

func (h *Handler) listPublicServices(c *gin.Context) {
	var query listPublicServicesQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	services, err := ListPublicServices(ctx, db.New(h.Pool), cmp.Or(query.Locale, "en"))
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, services)
}

// publicAvailability answers times only, never appointments or why time is blocked.
func (h *Handler) publicAvailability(c *gin.Context) {
	var query publicAvailabilityQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	availability, err := PublicAvailability(ctx, db.New(h.Pool), query.Service, query.From, query.To, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	response := availabilityResponse{
		Service:  query.Service,
		Timezone: availability.Timezone,
		From:     availability.First.Format(time.DateOnly),
		To:       availability.Last.Format(time.DateOnly),
		Days:     newSlotDayResponses(availability.Days),
	}
	c.JSON(http.StatusOK, response)
}

// requestAppointment stores a visitor's request, then queues its emails and hold expiry.
// The receipt never carries the id or the management token.
func (h *Handler) requestAppointment(c *gin.Context) {
	key, ok := web.ParseUUID(c, "Idempotency-Key", c.GetHeader("Idempotency-Key"))
	if !ok {
		return
	}
	var body requestAppointmentBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	// The key's hash covers the body as decoded, so a retry that differs only in spacing is the same request.
	encoded, err := json.Marshal(body)
	if err != nil {
		web.Error(c, err)
		return
	}
	request := Request{
		IdempotencyKey:      key.String(),
		Body:                encoded,
		Service:             body.Service,
		StartsAt:            body.StartsAt,
		Format:              body.Format,
		Locale:              body.Locale,
		VisitorName:         body.Visitor.Name,
		VisitorEmail:        body.Visitor.Email,
		VisitorPhone:        body.Visitor.Phone,
		VisitorNote:         body.Visitor.Note,
		PrivacyAcknowledged: body.PrivacyAcknowledged,
		PolicyAcknowledged:  body.PolicyAcknowledged,
	}
	requested, err := RequestAppointment(ctx, h.Pool, h.Secret, request, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, requested.Tasks...)
	slog.InfoContext(ctx, "appointment requested",
		"request_id", web.RequestID(ctx),
		"appointment_id", requested.AppointmentID.String(),
		"replayed", requested.Replayed,
	)
	if requested.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	c.JSON(http.StatusCreated, requested.Receipt)
}

// getManaged answers the same 404 for an unknown and an expired link.
func (h *Handler) getManaged(c *gin.Context) {
	var path tokenPath
	if !web.BindURI(c, &path) {
		return
	}
	ctx := c.Request.Context()

	managed, err := GetManaged(ctx, db.New(h.Pool), path.Token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, managed)
}

func (h *Handler) cancelManaged(c *gin.Context) {
	var path tokenPath
	if !web.BindURI(c, &path) {
		return
	}
	// The body is optional: a visitor may cancel without a message.
	var body cancelManagedBody
	if c.Request.ContentLength != 0 && !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	changed, err := CancelByClient(ctx, h.Pool, path.Token, body.Message, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logManaged(ctx, "appointment_cancelled_by_client")
	h.writeManaged(c, http.StatusOK, path.Token)
}

// requestManagedReschedule tells Daw Mi; the appointment keeps its time until she moves it.
func (h *Handler) requestManagedReschedule(c *gin.Context) {
	var path tokenPath
	if !web.BindURI(c, &path) {
		return
	}
	var body requestManagedRescheduleBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	changed, err := RequestReschedule(ctx, h.Pool, path.Token, body.PreferredTimes, body.Message, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logManaged(ctx, "reschedule_requested")
	h.writeManaged(c, http.StatusAccepted, path.Token)
}

func newSlotDayResponses(days []SlotDay) []slotDayResponse {
	responses := make([]slotDayResponse, len(days))
	for i, day := range days {
		responses[i] = slotDayResponse{
			Date:  day.Date.Format(time.DateOnly),
			Slots: make([]slotJSON, len(day.Slots)),
		}
		for j, slot := range day.Slots {
			responses[i].Slots[j] = slotJSON{StartsAt: slot.Start.UTC(), EndsAt: slot.End.UTC()}
		}
	}
	return responses
}

// writeManaged answers with what the link shows after the change.
func (h *Handler) writeManaged(c *gin.Context, status int, token string) {
	ctx := c.Request.Context()
	managed, err := GetManaged(ctx, db.New(h.Pool), token, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(status, managed)
}

// logManaged logs the event only: the token is as good as a password.
func logManaged(ctx context.Context, event string) {
	slog.InfoContext(ctx, event, "request_id", web.RequestID(ctx))
}
