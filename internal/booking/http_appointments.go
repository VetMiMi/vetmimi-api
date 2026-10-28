package booking

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listQuery struct {
	View      string   `form:"view" binding:"omitempty,oneof=upcoming pending past cancelled all"`
	Status    []string `form:"status" binding:"dive,oneof=pending confirmed completed declined cancelled_by_client cancelled_by_practitioner no_show expired"`
	ServiceID string   `form:"serviceId" binding:"omitempty,uuid"`
	From      string   `form:"from" binding:"omitempty,datetime=2006-01-02T15:04:05Z07:00"`
	To        string   `form:"to" binding:"omitempty,datetime=2006-01-02T15:04:05Z07:00"`
	Format    string   `form:"format" binding:"omitempty,oneof=online in_person"`
	Q         string   `form:"q" binding:"max=100"`
	Cursor    string   `form:"cursor" binding:"max=200"`
	Limit     *int     `form:"limit" binding:"omitempty,min=1,max=100"`
}

type manualInput struct {
	ServiceID     string       `json:"serviceId" binding:"required,uuid"`
	StartsAt      time.Time    `json:"startsAt" binding:"required"`
	Format        string       `json:"format" binding:"required,oneof=online in_person"`
	Locale        string       `json:"locale" binding:"required,oneof=en my"`
	Visitor       visitorInput `json:"visitor"`
	AdminNote     string       `json:"adminNote,omitempty" binding:"max=2000"`
	Status        string       `json:"status,omitempty" binding:"omitempty,oneof=confirmed pending"`
	NotifyVisitor *bool        `json:"notifyVisitor,omitempty"`
}

type actionInput struct {
	Version int32 `json:"version" binding:"required,min=1"`
}

// confirmInput accepts meetingLink, which has no domain support yet, and ignores it.
type confirmInput struct {
	Version     int32  `json:"version" binding:"required,min=1"`
	MeetingLink string `json:"meetingLink"`
}

type declineInput struct {
	Version          int32  `json:"version" binding:"required,min=1"`
	MessageToVisitor string `json:"messageToVisitor" binding:"max=1000"`
}

type rescheduleInput struct {
	Version       int32     `json:"version" binding:"required,min=1"`
	StartsAt      time.Time `json:"startsAt" binding:"required"`
	NotifyVisitor *bool     `json:"notifyVisitor"`
}

type cancelAppointmentInput struct {
	Version          int32  `json:"version" binding:"required,min=1"`
	MessageToVisitor string `json:"messageToVisitor" binding:"max=1000"`
	NotifyVisitor    *bool  `json:"notifyVisitor"`
}

// noteInput's Note is a pointer so an empty note, which clears it, still counts as given.
type noteInput struct {
	Version int32   `json:"version" binding:"required,min=1"`
	Note    *string `json:"note" binding:"required,max=2000"`
}

type appointmentServiceJSON struct {
	ID   pgtype.UUID     `json:"id"`
	Slug string          `json:"slug"`
	Name json.RawMessage `json:"name"`
}

type appointmentSummaryJSON struct {
	ID              pgtype.UUID            `json:"id"`
	Reference       string                 `json:"reference"`
	Status          string                 `json:"status"`
	Service         appointmentServiceJSON `json:"service"`
	StartsAt        time.Time              `json:"startsAt"`
	EndsAt          time.Time              `json:"endsAt"`
	DurationMinutes int32                  `json:"durationMinutes"`
	Timezone        string                 `json:"timezone"`
	Format          string                 `json:"format"`
	Source          string                 `json:"source"`
	VisitorName     string                 `json:"visitorName"`
	HoldExpiresAt   *time.Time             `json:"holdExpiresAt,omitempty"`
	CreatedAt       time.Time              `json:"createdAt"`
	UpdatedAt       time.Time              `json:"updatedAt"`
}

// appointmentDetailJSON is the admin's detail screen: every visitor field, the history and the messages.
type appointmentDetailJSON struct {
	appointmentSummaryJSON
	VisitorEmail          string              `json:"visitorEmail"`
	VisitorPhone          pgtype.Text         `json:"visitorPhone,omitzero"`
	VisitorNote           pgtype.Text         `json:"visitorNote,omitzero"`
	AdminNote             pgtype.Text         `json:"adminNote,omitzero"`
	Locale                string              `json:"locale"`
	MeetingLink           pgtype.Text         `json:"meetingLink,omitzero"`
	LateCancellation      bool                `json:"lateCancellation"`
	PrivacyAcknowledgedAt *time.Time          `json:"privacyAcknowledgedAt,omitempty"`
	PolicyAcknowledgedAt  *time.Time          `json:"policyAcknowledgedAt,omitempty"`
	Version               int32               `json:"version"`
	VideoRoom             *videoRoomJSON      `json:"videoRoom,omitempty"`
	AllowedActions        []string            `json:"allowedActions"`
	Events                []eventJSON         `json:"events"`
	Communications        []communicationJSON `json:"communications"`
}

type videoRoomJSON struct {
	ID        pgtype.UUID `json:"id"`
	State     string      `json:"state"`
	OpensAt   time.Time   `json:"opensAt"`
	ClosesAt  time.Time   `json:"closesAt"`
	StartedAt *time.Time  `json:"startedAt,omitempty"`
	EndedAt   *time.Time  `json:"endedAt,omitempty"`
}

type eventJSON struct {
	ID               int64           `json:"id"`
	Kind             string          `json:"kind"`
	FromStatus       pgtype.Text     `json:"fromStatus,omitzero"`
	ToStatus         pgtype.Text     `json:"toStatus,omitzero"`
	PreviousStartsAt *time.Time      `json:"previousStartsAt,omitempty"`
	PreviousEndsAt   *time.Time      `json:"previousEndsAt,omitempty"`
	NewStartsAt      *time.Time      `json:"newStartsAt,omitempty"`
	NewEndsAt        *time.Time      `json:"newEndsAt,omitempty"`
	Actor            string          `json:"actor"`
	ActorName        pgtype.Text     `json:"actorName,omitzero"`
	Detail           json.RawMessage `json:"detail"`
	CreatedAt        time.Time       `json:"createdAt"`
}

type appointmentListResponse struct {
	Timezone   string                   `json:"timezone"`
	Items      []appointmentSummaryJSON `json:"items"`
	NextCursor string                   `json:"nextCursor,omitempty"`
}

type dashboardResponse struct {
	Timezone          string                   `json:"timezone"`
	Counts            countsJSON               `json:"counts"`
	AttentionRequired []attentionJSON          `json:"attentionRequired"`
	Pending           []appointmentSummaryJSON `json:"pending"`
	Today             []appointmentSummaryJSON `json:"today"`
	Upcoming          []appointmentSummaryJSON `json:"upcoming"`
}

type countsJSON struct {
	Pending   int64 `json:"pending"`
	Confirmed int64 `json:"confirmed"`
	Today     int64 `json:"today"`
	ThisWeek  int64 `json:"thisWeek"`
}

type attentionJSON struct {
	Kind          string      `json:"kind"`
	AppointmentID pgtype.UUID `json:"appointmentId"`
	Reference     string      `json:"reference"`
	StartsAt      time.Time   `json:"startsAt"`
	Detail        string      `json:"detail"`
}

func (h *Handler) dashboard(c *gin.Context) {
	d, err := Dashboard(c.Request.Context(), db.New(h.Pool), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	attention := make([]attentionJSON, len(d.Attention))
	for i, a := range d.Attention {
		attention[i] = attentionJSON{Kind: a.Kind, AppointmentID: a.AppointmentID, Reference: a.Reference,
			StartsAt: a.StartsAt.UTC(), Detail: a.Detail}
	}
	c.JSON(http.StatusOK, dashboardResponse{
		Timezone: d.Timezone,
		Counts: countsJSON{Pending: d.Counts.Pending, Confirmed: d.Counts.Confirmed,
			Today: d.Counts.Today, ThisWeek: d.Counts.ThisWeek},
		AttentionRequired: attention,
		Pending:           summariesOf(d.Pending),
		Today:             summariesOf(d.Today),
		Upcoming:          summariesOf(d.Upcoming),
	})
}

func (h *Handler) listAppointments(c *gin.Context) {
	var q listQuery
	if !web.BindQuery(c, &q) {
		return
	}
	f := Filter{
		View:      View(q.View),
		ServiceID: uuidOf(q.ServiceID),
		From:      timeOf(q.From),
		To:        timeOf(q.To),
		Format:    q.Format,
		Search:    q.Q,
		Cursor:    q.Cursor,
	}
	for _, s := range q.Status {
		f.Statuses = append(f.Statuses, Status(s))
	}
	if q.Limit != nil {
		f.Limit = *q.Limit
	}
	page, err := ListAppointments(c.Request.Context(), db.New(h.Pool), f, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, appointmentListResponse{Timezone: page.Timezone, Items: summariesOf(page.Items),
		NextCursor: page.NextCursor})
}

func (h *Handler) getAppointment(c *gin.Context) {
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	h.writeDetail(c, http.StatusOK, id)
}

// createManualAppointment stores an appointment Daw Mi made by hand; a replay answers with the same appointment.
func (h *Handler) createManualAppointment(c *gin.Context) {
	key, err := uuid.Parse(c.GetHeader("Idempotency-Key"))
	if err != nil {
		web.Error(c, apperr.Invalid("Idempotency-Key must be a UUID.",
			apperr.FieldError{Field: "Idempotency-Key", Message: "must be a valid uuid"}))
		return
	}
	var in manualInput
	if !web.BindJSON(c, &in) {
		return
	}
	body, err := json.Marshal(in)
	if err != nil {
		web.Error(c, err)
		return
	}
	ctx := c.Request.Context()
	res, err := CreateManual(ctx, h.Pool, h.Secret, Manual{
		IdempotencyKey: key.String(),
		Body:           body,
		ServiceID:      uuidOf(in.ServiceID),
		StartsAt:       in.StartsAt,
		Format:         in.Format,
		Locale:         in.Locale,
		VisitorName:    in.Visitor.Name,
		VisitorEmail:   in.Visitor.Email,
		VisitorPhone:   in.Visitor.Phone,
		VisitorNote:    in.Visitor.Note,
		AdminNote:      in.AdminNote,
		Status:         Status(cmp.Or(in.Status, string(Confirmed))),
		Notify:         in.NotifyVisitor == nil || *in.NotifyVisitor,
		Actor:          adminID(ctx),
	}, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterAdminChange(ctx, "appointment_created", res.AppointmentID, Changed{Tasks: res.Tasks})
	if res.Replayed {
		c.Header("Idempotent-Replayed", "true")
	}
	h.writeDetail(c, http.StatusCreated, res.AppointmentID)
}

func (h *Handler) confirmAppointment(c *gin.Context) {
	var in confirmInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	changed, err := Confirm(c.Request.Context(), h.Pool, h.Secret, change, h.Now())
	h.answerChange(c, "appointment_confirmed", change.ID, changed, err)
}

func (h *Handler) declineAppointment(c *gin.Context) {
	var in declineInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	change.ToVisitor = in.MessageToVisitor
	changed, err := Decline(c.Request.Context(), h.Pool, change, h.Now())
	h.answerChange(c, "appointment_declined", change.ID, changed, err)
}

func (h *Handler) rescheduleAppointment(c *gin.Context) {
	var in rescheduleInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	change.Notify = in.NotifyVisitor == nil || *in.NotifyVisitor
	changed, err := Reschedule(c.Request.Context(), h.Pool, change, in.StartsAt, h.Now())
	h.answerChange(c, "appointment_rescheduled", change.ID, changed, err)
}

func (h *Handler) cancelAppointment(c *gin.Context) {
	var in cancelAppointmentInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	change.ToVisitor = in.MessageToVisitor
	change.Notify = in.NotifyVisitor == nil || *in.NotifyVisitor
	changed, err := Cancel(c.Request.Context(), h.Pool, change, h.Now())
	h.answerChange(c, "appointment_cancelled", change.ID, changed, err)
}

func (h *Handler) completeAppointment(c *gin.Context) {
	h.recordOutcome(c, "appointment_completed", Complete)
}

func (h *Handler) markNoShow(c *gin.Context) {
	h.recordOutcome(c, "appointment_no_show", MarkNoShow)
}

func (h *Handler) recordOutcome(c *gin.Context, event string,
	action func(context.Context, *pgxpool.Pool, Change, time.Time) (Changed, error)) {
	var in actionInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	changed, err := action(c.Request.Context(), h.Pool, change, h.Now())
	h.answerChange(c, event, change.ID, changed, err)
}

func (h *Handler) setAppointmentNote(c *gin.Context) {
	var in noteInput
	id, ok := web.PathUUID(c, "appointmentId")
	if !ok || !web.BindJSON(c, &in) {
		return
	}
	change := changeOf(c, id, in.Version)
	err := SetNote(c.Request.Context(), h.Pool, change, *in.Note, h.Now())
	h.answerChange(c, "appointment_note_set", change.ID, Changed{}, err)
}

func changeOf(c *gin.Context, id pgtype.UUID, version int32) Change {
	return Change{ID: id, Version: version, Actor: adminID(c.Request.Context()), Notify: true}
}

// answerChange applies a committed change and answers with the fresh detail.
func (h *Handler) answerChange(c *gin.Context, event string, id pgtype.UUID, changed Changed, err error) {
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterAdminChange(c.Request.Context(), event, id, changed)
	h.writeDetail(c, http.StatusOK, id)
}

// afterAdminChange logs the appointment by id only.
func (h *Handler) afterAdminChange(ctx context.Context, event string, id pgtype.UUID, changed Changed) {
	h.afterCommit(ctx, changed)
	slog.InfoContext(ctx, event, "request_id", web.RequestID(ctx), "appointment_id", id.String())
}

func (h *Handler) writeDetail(c *gin.Context, status int, id pgtype.UUID) {
	d, err := GetAppointment(c.Request.Context(), db.New(h.Pool), id, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(status, detailOf(d))
}

func detailOf(d Detail) appointmentDetailJSON {
	a := d.Appointment
	out := appointmentDetailJSON{
		appointmentSummaryJSON: appointmentSummaryJSON{
			ID:              a.ID,
			Reference:       a.Reference,
			Status:          a.Status,
			Service:         appointmentServiceJSON{ID: a.ServiceID, Slug: a.ServiceSlug, Name: a.ServiceName},
			StartsAt:        a.StartsAt.UTC(),
			EndsAt:          a.EndsAt.UTC(),
			DurationMinutes: a.DurationMinutes,
			Timezone:        a.Timezone,
			Format:          a.Format,
			Source:          a.Source,
			VisitorName:     a.VisitorName,
			HoldExpiresAt:   utcOrNil(a.HoldExpiresAt),
			CreatedAt:       a.CreatedAt.UTC(),
			UpdatedAt:       a.UpdatedAt.UTC(),
		},
		VisitorEmail:          a.VisitorEmail,
		VisitorPhone:          a.VisitorPhone,
		VisitorNote:           a.VisitorNote,
		AdminNote:             a.AdminNote,
		Locale:                a.Locale,
		MeetingLink:           a.MeetingLink,
		LateCancellation:      a.LateCancellation,
		PrivacyAcknowledgedAt: utcOrNil(a.PrivacyAckAt),
		PolicyAcknowledgedAt:  utcOrNil(a.PolicyAckAt),
		Version:               a.Version,
		AllowedActions:        d.AllowedActions,
		Events:                make([]eventJSON, len(d.Events)),
		Communications:        make([]communicationJSON, len(d.Communications)),
	}
	if r := d.VideoRoom; r != nil {
		out.VideoRoom = &videoRoomJSON{ID: r.ID, State: r.State, OpensAt: r.OpensAt.UTC(), ClosesAt: r.ClosesAt.UTC(),
			StartedAt: utcOrNil(r.StartedAt), EndedAt: utcOrNil(r.EndedAt)}
	}
	for i, e := range d.Events {
		out.Events[i] = eventOf(e)
	}
	for i, m := range d.Communications {
		out.Communications[i] = communicationOf(m)
	}
	return out
}

func eventOf(e db.ListAppointmentEventsRow) eventJSON {
	out := eventJSON{ID: e.ID, Kind: e.Kind, FromStatus: e.FromStatus, ToStatus: e.ToStatus, Actor: e.Actor,
		ActorName: e.ActorName, Detail: e.Detail, CreatedAt: e.CreatedAt.UTC()}
	if e.PreviousRange.Valid {
		out.PreviousStartsAt, out.PreviousEndsAt = rangeOf(e.PreviousRange)
	}
	if e.NewRange.Valid {
		out.NewStartsAt, out.NewEndsAt = rangeOf(e.NewRange)
	}
	return out
}

func rangeOf(r pgtype.Range[pgtype.Timestamptz]) (*time.Time, *time.Time) {
	start, end := r.Lower.Time.UTC(), r.Upper.Time.UTC()
	return &start, &end
}

func summariesOf(rows []db.ListAppointmentsRow) []appointmentSummaryJSON {
	out := make([]appointmentSummaryJSON, len(rows))
	for i, r := range rows {
		out[i] = appointmentSummaryJSON{
			ID:              r.ID,
			Reference:       r.Reference,
			Status:          r.Status,
			Service:         appointmentServiceJSON{ID: r.ServiceID, Slug: r.ServiceSlug, Name: r.ServiceName},
			StartsAt:        r.StartsAt.UTC(),
			EndsAt:          r.EndsAt.UTC(),
			DurationMinutes: r.DurationMinutes,
			Timezone:        r.Timezone,
			Format:          r.Format,
			Source:          r.Source,
			VisitorName:     r.VisitorName,
			HoldExpiresAt:   utcOrNil(r.HoldExpiresAt),
			CreatedAt:       r.CreatedAt.UTC(),
			UpdatedAt:       r.UpdatedAt.UTC(),
		}
	}
	return out
}

func utcOrNil(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	utc := t.Time.UTC()
	return &utc
}

// uuidOf reads an id binding has checked; an absent one is NULL.
func uuidOf(s string) pgtype.UUID {
	id, err := uuid.Parse(s)
	return pgtype.UUID{Bytes: id, Valid: err == nil}
}

// timeOf reads a time binding has checked; an absent one is zero, which filters nothing.
func timeOf(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// adminID is the signed-in admin; the Admin group has already checked the session.
func adminID(ctx context.Context) pgtype.UUID {
	session, _ := auth.FromContext(ctx)
	return session.User.ID
}
