package booking

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type markCommunicatedBody struct {
	Kind string `json:"kind" binding:"required,oneof=request_received booking_confirmed request_declined rescheduled cancelled reminder request_expired practitioner_new_request practitioner_new_booking practitioner_client_cancelled practitioner_reschedule_requested practitioner_new_enquiry"`
	Note string `json:"note" binding:"required,max=500"`
}

// communicationResponse leaves out Daw Mi's message to the visitor, which the contract has no field for.
type communicationResponse struct {
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
	communicationID, ok := web.PathUUID(c, "communicationId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	communication, task, err := comms.Resend(ctx, h.Pool, appointmentID, communicationID, currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	h.Queue.Enqueue(ctx, task)
	slog.InfoContext(ctx, "communication resent",
		"request_id", web.RequestID(ctx),
		"communication_id", communication.ID.String(),
		"resend_of", communicationID.String(),
	)
	c.JSON(http.StatusCreated, newCommunicationResponse(communication))
}

// markCommunicated records that Daw Mi told the visitor herself, for example by phone.
func (h *Handler) markCommunicated(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}
	var body markCommunicatedBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	communication, err := comms.MarkCommunicated(ctx, db.New(h.Pool), appointmentID, comms.Kind(body.Kind), body.Note,
		currentUserID(c), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "communication recorded",
		"request_id", web.RequestID(ctx),
		"appointment_id", appointmentID.String(),
		"communication_id", communication.ID.String(),
	)
	c.JSON(http.StatusCreated, newCommunicationResponse(communication))
}

func newCommunicationResponse(communication db.Communication) communicationResponse {
	return communicationResponse{
		ID:            communication.ID,
		AppointmentID: communication.AppointmentID,
		Kind:          communication.Kind,
		Audience:      communication.Audience,
		Channel:       communication.Channel,
		Recipient:     communication.Recipient,
		Locale:        communication.Locale,
		Status:        communication.Status,
		ScheduledFor:  communication.ScheduledFor.UTC(),
		SentAt:        utcOrNil(communication.SentAt),
		Error:         communication.Error,
		Attempts:      communication.Attempts,
		ResendOf:      communication.ResendOf,
		Note:          communication.Note,
		CreatedAt:     communication.CreatedAt.UTC(),
	}
}
