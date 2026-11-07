package booking

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

type dashboardResponse struct {
	Timezone          string                       `json:"timezone"`
	Counts            countsResponse               `json:"counts"`
	AttentionRequired []AttentionItem              `json:"attentionRequired"`
	Pending           []appointmentSummaryResponse `json:"pending"`
	Today             []appointmentSummaryResponse `json:"today"`
	Upcoming          []appointmentSummaryResponse `json:"upcoming"`
}

type countsResponse struct {
	Pending   int64 `json:"pending"`
	Confirmed int64 `json:"confirmed"`
	Today     int64 `json:"today"`
	ThisWeek  int64 `json:"thisWeek"`
}

func (h *Handler) dashboard(c *gin.Context) {
	ctx := c.Request.Context()

	dashboard, err := Dashboard(ctx, db.New(h.Pool), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newDashboardResponse(dashboard))
}

func newDashboardResponse(dashboard DashboardData) dashboardResponse {
	return dashboardResponse{
		Timezone: dashboard.Timezone,
		Counts: countsResponse{
			Pending:   dashboard.Counts.Pending,
			Confirmed: dashboard.Counts.Confirmed,
			Today:     dashboard.Counts.Today,
			ThisWeek:  dashboard.Counts.ThisWeek,
		},
		AttentionRequired: dashboard.Attention,
		Pending:           newAppointmentSummaryResponses(dashboard.Pending),
		Today:             newAppointmentSummaryResponses(dashboard.Today),
		Upcoming:          newAppointmentSummaryResponses(dashboard.Upcoming),
	}
}
