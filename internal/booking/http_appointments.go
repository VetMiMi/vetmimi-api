package booking

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listAppointmentsQuery struct {
	View      string   `form:"view" binding:"omitempty,oneof=upcoming pending past cancelled all"`
	Status    []string `form:"status" binding:"dive,oneof=pending confirmed completed declined cancelled_by_client cancelled_by_practitioner no_show expired"`
	ServiceID string   `form:"serviceId" binding:"omitempty,uuid"`
	From      string   `form:"from" binding:"omitempty,datetime=2006-01-02T15:04:05Z07:00"`
	To        string   `form:"to" binding:"omitempty,datetime=2006-01-02T15:04:05Z07:00"`
	Format    string   `form:"format" binding:"omitempty,oneof=online in_person"`
	Search    string   `form:"q" binding:"max=100"`
	Cursor    string   `form:"cursor" binding:"max=200"`
	// Limit is read by limitOf: gin's int binding fails without naming the parameter.
	Limit string `form:"limit"`
}

// createManualAppointmentBody's fields keep their order: the idempotency key's hash covers the body as re-encoded.
type createManualAppointmentBody struct {
	ServiceID     string      `json:"serviceId" binding:"required,uuid"`
	StartsAt      time.Time   `json:"startsAt" binding:"required"`
	Format        string      `json:"format" binding:"required,oneof=online in_person"`
	Locale        string      `json:"locale" binding:"required,oneof=en my"`
	Visitor       visitorBody `json:"visitor"`
	AdminNote     string      `json:"adminNote,omitempty" binding:"max=2000"`
	Status        string      `json:"status,omitempty" binding:"omitempty,oneof=confirmed pending"`
	NotifyVisitor *bool       `json:"notifyVisitor,omitempty"`
}

type appointmentListResponse struct {
	Timezone   string                       `json:"timezone"`
	Items      []appointmentSummaryResponse `json:"items"`
	NextCursor string                       `json:"nextCursor,omitempty"`
}

type appointmentServiceResponse struct {
	ID   pgtype.UUID     `json:"id"`
	Slug string          `json:"slug"`
	Name json.RawMessage `json:"name"`
}

// appointmentSummaryResponse carries the visitor's name, never their email or notes.
type appointmentSummaryResponse struct {
	ID              pgtype.UUID                `json:"id"`
	Reference       string                     `json:"reference"`
	Status          string                     `json:"status"`
	Service         appointmentServiceResponse `json:"service"`
	StartsAt        time.Time                  `json:"startsAt"`
	EndsAt          time.Time                  `json:"endsAt"`
	DurationMinutes int32                      `json:"durationMinutes"`
	Timezone        string                     `json:"timezone"`
	Format          string                     `json:"format"`
	Source          string                     `json:"source"`
	VisitorName     string                     `json:"visitorName"`
	HoldExpiresAt   *time.Time                 `json:"holdExpiresAt,omitempty"`
	CreatedAt       time.Time                  `json:"createdAt"`
	UpdatedAt       time.Time                  `json:"updatedAt"`
}

func (h *Handler) listAppointments(c *gin.Context) {
	var query listAppointmentsQuery
	if !web.BindQuery(c, &query) {
		return
	}
	limit, ok := limitOf(c, query.Limit)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	filter := Filter{
		View:      View(query.View),
		ServiceID: uuidOf(query.ServiceID),
		From:      timeOf(query.From),
		To:        timeOf(query.To),
		Format:    query.Format,
		Search:    query.Search,
		Cursor:    query.Cursor,
		Limit:     limit,
	}
	for _, status := range query.Status {
		filter.Statuses = append(filter.Statuses, Status(status))
	}
	page, err := ListAppointments(ctx, db.New(h.Pool), filter, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	response := appointmentListResponse{
		Timezone:   page.Timezone,
		Items:      newAppointmentSummaryResponses(page.Items),
		NextCursor: page.NextCursor,
	}
	c.JSON(http.StatusOK, response)
}

// createManualAppointment stores an appointment Daw Mi made by hand; a replay answers with the same appointment.
func (h *Handler) createManualAppointment(c *gin.Context) {
	key, ok := web.ParseUUID(c, "Idempotency-Key", c.GetHeader("Idempotency-Key"))
	if !ok {
		return
	}
	var body createManualAppointmentBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	encoded, err := json.Marshal(body)
	if err != nil {
		web.Error(c, err)
		return
	}
	manual := Manual{
		IdempotencyKey: key.String(),
		Body:           encoded,
		ServiceID:      uuidOf(body.ServiceID),
		StartsAt:       body.StartsAt,
		Format:         body.Format,
		Locale:         body.Locale,
		VisitorName:    body.Visitor.Name,
		VisitorEmail:   body.Visitor.Email,
		VisitorPhone:   body.Visitor.Phone,
		VisitorNote:    body.Visitor.Note,
		AdminNote:      body.AdminNote,
		Status:         Status(cmp.Or(body.Status, string(Confirmed))),
		Notify:         body.NotifyVisitor == nil || *body.NotifyVisitor,
		Actor:          currentUserID(c),
	}
	created, err := CreateManual(ctx, h.Pool, h.Secret, manual, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, created.Tasks...)
	logAppointment(ctx, "appointment_created", created.AppointmentID)
	if created.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	h.writeDetail(c, http.StatusCreated, created.AppointmentID)
}

func newAppointmentSummaryResponses(rows []db.ListAppointmentsRow) []appointmentSummaryResponse {
	responses := make([]appointmentSummaryResponse, len(rows))
	for i, row := range rows {
		responses[i] = appointmentSummaryResponse{
			ID:        row.ID,
			Reference: row.Reference,
			Status:    row.Status,
			Service: appointmentServiceResponse{
				ID:   row.ServiceID,
				Slug: row.ServiceSlug,
				Name: row.ServiceName,
			},
			StartsAt:        row.StartsAt.UTC(),
			EndsAt:          row.EndsAt.UTC(),
			DurationMinutes: row.DurationMinutes,
			Timezone:        row.Timezone,
			Format:          row.Format,
			Source:          row.Source,
			VisitorName:     row.VisitorName,
			HoldExpiresAt:   utcOrNil(row.HoldExpiresAt),
			CreatedAt:       row.CreatedAt.UTC(),
			UpdatedAt:       row.UpdatedAt.UTC(),
		}
	}
	return responses
}

// afterCommit hands the queue what a committed change asks of it and closes a video room it ended.
func (h *Handler) afterCommit(ctx context.Context, changed Changed) {
	// Remove and Replace, unlike Enqueue, need a real queue; tests run without one.
	if h.Queue != nil {
		h.Queue.Remove(changed.Remove...)
		h.Queue.Replace(ctx, changed.Replace...)
	}
	h.Queue.Enqueue(ctx, changed.Tasks...)
	if h.Hub != nil {
		h.Hub.EndRoom(changed.EndedRoom)
	}
}

// logAppointment logs the appointment by id only.
func logAppointment(ctx context.Context, event string, appointmentID pgtype.UUID) {
	slog.InfoContext(ctx, event, "request_id", web.RequestID(ctx), "appointment_id", appointmentID.String())
}

// currentUserID is safe on admin routes: the Admin group has already checked the session.
func currentUserID(c *gin.Context) pgtype.UUID {
	session, _ := auth.FromContext(c.Request.Context())
	return session.User.ID
}

// uuidOf reads an id binding has checked; an absent one is NULL.
func uuidOf(s string) pgtype.UUID {
	id, err := uuid.Parse(s)
	return pgtype.UUID{Bytes: id, Valid: err == nil}
}

// limitOf reads the page size; an absent one is zero, which takes the default.
func limitOf(c *gin.Context, s string) (int, bool) {
	if s == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(s)
	var problem string
	switch {
	case err != nil:
		problem = "must be of type integer"
	case limit < 1:
		problem = "must be at least 1"
	case limit > 100:
		problem = "must be at most 100"
	default:
		return limit, true
	}
	web.Error(c, apperr.Invalid("The request is not valid.", apperr.FieldError{Field: "limit", Message: problem}))
	return 0, false
}

// timeOf reads a time binding has checked, so parsing cannot fail; an absent one is zero, which filters nothing.
func timeOf(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func utcOrNil(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return utcPointer(t.Time)
}

func utcPointer(t time.Time) *time.Time {
	utc := t.UTC()
	return &utc
}
