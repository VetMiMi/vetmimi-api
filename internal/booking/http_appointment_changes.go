package booking

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// confirmBody accepts meetingLink, which has no domain support yet, and ignores it.
type confirmBody struct {
	Version     int32  `json:"version" binding:"required,min=1"`
	MeetingLink string `json:"meetingLink"`
}

type declineBody struct {
	Version          int32  `json:"version" binding:"required,min=1"`
	MessageToVisitor string `json:"messageToVisitor" binding:"max=1000"`
}

type rescheduleBody struct {
	Version       int32     `json:"version" binding:"required,min=1"`
	StartsAt      time.Time `json:"startsAt" binding:"required"`
	NotifyVisitor *bool     `json:"notifyVisitor"`
}

type cancelAppointmentBody struct {
	Version          int32  `json:"version" binding:"required,min=1"`
	MessageToVisitor string `json:"messageToVisitor" binding:"max=1000"`
	NotifyVisitor    *bool  `json:"notifyVisitor"`
}

type outcomeBody struct {
	Version int32 `json:"version" binding:"required,min=1"`
}

// setNoteBody's Note is a pointer so an empty note, which clears it, still counts as given.
type setNoteBody struct {
	Version int32   `json:"version" binding:"required,min=1"`
	Note    *string `json:"note" binding:"required,max=2000"`
}

func (h *Handler) confirmAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body confirmBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:      appointmentID,
		Version: body.Version,
		Actor:   currentUserID(c),
		Notify:  true,
	}
	changed, err := Confirm(ctx, h.Pool, h.Secret, change, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_confirmed", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) declineAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body declineBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:        appointmentID,
		Version:   body.Version,
		Actor:     currentUserID(c),
		ToVisitor: body.MessageToVisitor,
		Notify:    true,
	}
	changed, err := Decline(ctx, h.Pool, change, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_declined", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) rescheduleAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body rescheduleBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:      appointmentID,
		Version: body.Version,
		Actor:   currentUserID(c),
		Notify:  body.NotifyVisitor == nil || *body.NotifyVisitor,
	}
	changed, err := Reschedule(ctx, h.Pool, change, body.StartsAt, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_rescheduled", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) cancelAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body cancelAppointmentBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:        appointmentID,
		Version:   body.Version,
		Actor:     currentUserID(c),
		ToVisitor: body.MessageToVisitor,
		Notify:    body.NotifyVisitor == nil || *body.NotifyVisitor,
	}
	changed, err := Cancel(ctx, h.Pool, change, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_cancelled", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) completeAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body outcomeBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:      appointmentID,
		Version: body.Version,
		Actor:   currentUserID(c),
		Notify:  true,
	}
	changed, err := Complete(ctx, h.Pool, change, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_completed", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) markNoShow(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body outcomeBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:      appointmentID,
		Version: body.Version,
		Actor:   currentUserID(c),
		Notify:  true,
	}
	changed, err := MarkNoShow(ctx, h.Pool, change, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.afterCommit(ctx, changed)
	logAppointment(ctx, "appointment_no_show", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}

func (h *Handler) setAppointmentNote(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body setNoteBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	change := Change{
		ID:      appointmentID,
		Version: body.Version,
		Actor:   currentUserID(c),
		Notify:  true,
	}
	err := SetNote(ctx, h.Pool, change, *body.Note, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logAppointment(ctx, "appointment_note_set", appointmentID)
	h.writeDetail(c, http.StatusOK, appointmentID)
}
