package comms

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type listCommunicationsQuery struct {
	Status string `form:"status" binding:"omitempty,oneof=queued sent failed cancelled"`
	Search string `form:"q" binding:"max=100"`
	Cursor string `form:"cursor" binding:"max=200"`
	Limit  *int   `form:"limit" binding:"omitnil,min=1,max=100"`
}

type communicationListResponse struct {
	Items      []communicationResponse `json:"items"`
	NextCursor string                  `json:"nextCursor,omitempty"`
}

// communicationResponse is an email as the appointment page shows it, plus the
// appointment's reference and the visitor's name.
type communicationResponse struct {
	ID                   pgtype.UUID `json:"id"`
	AppointmentID        pgtype.UUID `json:"appointmentId"`
	AppointmentReference string      `json:"appointmentReference"`
	VisitorName          string      `json:"visitorName"`
	Kind                 string      `json:"kind"`
	Audience             string      `json:"audience"`
	Channel              string      `json:"channel"`
	Recipient            pgtype.Text `json:"recipient,omitzero"`
	Locale               string      `json:"locale"`
	Status               string      `json:"status"`
	ScheduledFor         time.Time   `json:"scheduledFor"`
	SentAt               *time.Time  `json:"sentAt,omitempty"`
	Error                pgtype.Text `json:"error,omitzero"`
	Attempts             int32       `json:"attempts"`
	ResendOf             pgtype.UUID `json:"resendOf,omitzero"`
	CreatedAt            time.Time   `json:"createdAt"`
}

func (h *Handler) listCommunications(c *gin.Context) {
	var query listCommunicationsQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	filter := CommunicationFilter{
		Status: Status(query.Status),
		Search: query.Search,
		Cursor: query.Cursor,
	}
	if query.Limit != nil {
		filter.Limit = *query.Limit
	}
	page, err := ListCommunications(ctx, db.New(h.Pool), filter)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := communicationListResponse{
		Items:      make([]communicationResponse, len(page.Items)),
		NextCursor: page.NextCursor,
	}
	for i, row := range page.Items {
		response.Items[i] = newCommunicationResponse(row)
	}
	c.JSON(http.StatusOK, response)
}

func newCommunicationResponse(row db.ListCommunicationsRow) communicationResponse {
	response := communicationResponse{
		ID:                   row.ID,
		AppointmentID:        row.AppointmentID,
		AppointmentReference: row.AppointmentReference,
		VisitorName:          row.VisitorName,
		Kind:                 row.Kind,
		Audience:             row.Audience,
		Channel:              row.Channel,
		Recipient:            row.Recipient,
		Locale:               row.Locale,
		Status:               row.Status,
		ScheduledFor:         row.ScheduledFor.UTC(),
		Error:                row.Error,
		Attempts:             row.Attempts,
		ResendOf:             row.ResendOf,
		CreatedAt:            row.CreatedAt.UTC(),
	}
	if row.SentAt.Valid {
		response.SentAt = new(row.SentAt.Time.UTC())
	}
	return response
}
