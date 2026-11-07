package booking

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// appointmentDetailResponse is the admin's detail screen: every visitor field, the history and the messages.
type appointmentDetailResponse struct {
	appointmentSummaryResponse
	VisitorEmail          string                  `json:"visitorEmail"`
	VisitorPhone          pgtype.Text             `json:"visitorPhone,omitzero"`
	VisitorNote           pgtype.Text             `json:"visitorNote,omitzero"`
	AdminNote             pgtype.Text             `json:"adminNote,omitzero"`
	Locale                string                  `json:"locale"`
	MeetingLink           pgtype.Text             `json:"meetingLink,omitzero"`
	LateCancellation      bool                    `json:"lateCancellation"`
	PrivacyAcknowledgedAt *time.Time              `json:"privacyAcknowledgedAt,omitempty"`
	PolicyAcknowledgedAt  *time.Time              `json:"policyAcknowledgedAt,omitempty"`
	Version               int32                   `json:"version"`
	VideoRoom             *videoRoomResponse      `json:"videoRoom,omitempty"`
	AllowedActions        []string                `json:"allowedActions"`
	Events                []eventResponse         `json:"events"`
	Communications        []communicationResponse `json:"communications"`
}

type videoRoomResponse struct {
	ID        pgtype.UUID `json:"id"`
	State     string      `json:"state"`
	OpensAt   time.Time   `json:"opensAt"`
	ClosesAt  time.Time   `json:"closesAt"`
	StartedAt *time.Time  `json:"startedAt,omitempty"`
	EndedAt   *time.Time  `json:"endedAt,omitempty"`
}

type eventResponse struct {
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

func (h *Handler) getAppointment(c *gin.Context) {
	appointmentID, ok := web.PathUUID(c, "appointmentId")
	if !ok {
		return
	}

	h.writeDetail(c, http.StatusOK, appointmentID)
}

func newAppointmentDetailResponse(detail Detail) appointmentDetailResponse {
	appointment := detail.Appointment
	response := appointmentDetailResponse{
		appointmentSummaryResponse: appointmentSummaryResponse{
			ID:        appointment.ID,
			Reference: appointment.Reference,
			Status:    appointment.Status,
			Service: appointmentServiceResponse{
				ID:   appointment.ServiceID,
				Slug: appointment.ServiceSlug,
				Name: appointment.ServiceName,
			},
			StartsAt:        appointment.StartsAt.UTC(),
			EndsAt:          appointment.EndsAt.UTC(),
			DurationMinutes: appointment.DurationMinutes,
			Timezone:        appointment.Timezone,
			Format:          appointment.Format,
			Source:          appointment.Source,
			VisitorName:     appointment.VisitorName,
			HoldExpiresAt:   utcOrNil(appointment.HoldExpiresAt),
			CreatedAt:       appointment.CreatedAt.UTC(),
			UpdatedAt:       appointment.UpdatedAt.UTC(),
		},
		VisitorEmail:          appointment.VisitorEmail,
		VisitorPhone:          appointment.VisitorPhone,
		VisitorNote:           appointment.VisitorNote,
		AdminNote:             appointment.AdminNote,
		Locale:                appointment.Locale,
		MeetingLink:           appointment.MeetingLink,
		LateCancellation:      appointment.LateCancellation,
		PrivacyAcknowledgedAt: utcOrNil(appointment.PrivacyAckAt),
		PolicyAcknowledgedAt:  utcOrNil(appointment.PolicyAckAt),
		Version:               appointment.Version,
		AllowedActions:        detail.AllowedActions,
		Events:                make([]eventResponse, len(detail.Events)),
		Communications:        make([]communicationResponse, len(detail.Communications)),
	}
	room := detail.VideoRoom
	if room != nil {
		response.VideoRoom = &videoRoomResponse{
			ID:        room.ID,
			State:     room.State,
			OpensAt:   room.OpensAt.UTC(),
			ClosesAt:  room.ClosesAt.UTC(),
			StartedAt: utcOrNil(room.StartedAt),
			EndedAt:   utcOrNil(room.EndedAt),
		}
	}
	for i, event := range detail.Events {
		response.Events[i] = newEventResponse(event)
	}
	for i, communication := range detail.Communications {
		response.Communications[i] = newCommunicationResponse(communication)
	}
	return response
}

func newEventResponse(event db.ListAppointmentEventsRow) eventResponse {
	response := eventResponse{
		ID:         event.ID,
		Kind:       event.Kind,
		FromStatus: event.FromStatus,
		ToStatus:   event.ToStatus,
		Actor:      event.Actor,
		ActorName:  event.ActorName,
		Detail:     event.Detail,
		CreatedAt:  event.CreatedAt.UTC(),
	}
	if event.PreviousRange.Valid {
		response.PreviousStartsAt = utcPointer(event.PreviousRange.Lower.Time)
		response.PreviousEndsAt = utcPointer(event.PreviousRange.Upper.Time)
	}
	if event.NewRange.Valid {
		response.NewStartsAt = utcPointer(event.NewRange.Lower.Time)
		response.NewEndsAt = utcPointer(event.NewRange.Upper.Time)
	}
	return response
}

// writeDetail answers with the appointment as it is now, after any change.
func (h *Handler) writeDetail(c *gin.Context, status int, appointmentID pgtype.UUID) {
	ctx := c.Request.Context()
	detail, err := GetAppointment(ctx, db.New(h.Pool), appointmentID, h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(status, newAppointmentDetailResponse(detail))
}
