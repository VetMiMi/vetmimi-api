package booking

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/queue"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type markCommunicatedInput struct {
	Kind string `json:"kind" binding:"required,oneof=request_received booking_confirmed request_declined rescheduled cancelled reminder request_expired practitioner_new_request practitioner_new_booking practitioner_client_cancelled practitioner_reschedule_requested practitioner_new_enquiry"`
	Note string `json:"note" binding:"required,max=500"`
}

// communicationJSON leaves out Daw Mi's message to the visitor, which the contract has no field for.
type communicationJSON struct {
	ID            pgtype.UUID `json:"id"`
	AppointmentID pgtype.UUID `json:"appointmentId,omitzero"`
	Kind          string      `json:"kind"`
	Audience      string      `json:"audience"`
	Channel       string      `json:"channel"`
	Recipient     pgtype.Text `json:"recipient,omitzero"`
	Locale        string      `json:"locale"`
	Status        string      `json:"status"`
	ScheduledFor  time.Time   `json:"scheduledFor"`
	SentAt        *time.Time  `json:"sentAt,omitempty"`
	Error         pgtype.Text `json:"error,omitzero"`
	Attempts      int32       `json:"attempts"`
	ResendOf      pgtype.UUID `json:"resendOf,omitzero"`
	Note          pgtype.Text `json:"note,omitzero"`
	CreatedAt     time.Time   `json:"createdAt"`
}

// resendCommunication queues a copy; the original stays as it was.
func (h *Handler) resendCommunication(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	id, ok := web.PathUUID(c, "communicationId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	row, task, err := comms.Resend(ctx, h.Pool, appointmentID, id, adminID(ctx), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	h.afterCommit(ctx, Changed{Tasks: []queue.Task{task}})
	slog.InfoContext(ctx, "communication resent", "request_id", web.RequestID(ctx),
		"communication_id", row.ID.String(), "resend_of", id.String())
	c.JSON(http.StatusCreated, communicationOf(row))
}

// markCommunicated records that Daw Mi told the visitor herself, for example by phone.
func (h *Handler) markCommunicated(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var in markCommunicatedInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	row, err := comms.MarkCommunicated(ctx, db.New(h.Pool), appointmentID, comms.Kind(in.Kind), in.Note,
		adminID(ctx), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "communication recorded", "request_id", web.RequestID(ctx),
		"appointment_id", appointmentID.String(), "communication_id", row.ID.String())
	c.JSON(http.StatusCreated, communicationOf(row))
}

func communicationOf(m db.Communication) communicationJSON {
	return communicationJSON{
		ID:            m.ID,
		AppointmentID: m.AppointmentID,
		Kind:          m.Kind,
		Audience:      m.Audience,
		Channel:       m.Channel,
		Recipient:     m.Recipient,
		Locale:        m.Locale,
		Status:        m.Status,
		ScheduledFor:  m.ScheduledFor.UTC(),
		SentAt:        utcOrNil(m.SentAt),
		Error:         m.Error,
		Attempts:      m.Attempts,
		ResendOf:      m.ResendOf,
		Note:          m.Note,
		CreatedAt:     m.CreatedAt.UTC(),
	}
}
