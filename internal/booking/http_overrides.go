package booking

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// listOverridesQuery is optional local dates; the rules default to the next 62 days.
type listOverridesQuery struct {
	From string `form:"from" binding:"omitempty,datetime=2006-01-02"`
	To   string `form:"to" binding:"omitempty,datetime=2006-01-02"`
}

// Dates and times are bound as strings so that a bad one is reported by its field name.
type overrideBody struct {
	OnDate   string  `json:"onDate" binding:"required,datetime=2006-01-02"`
	Kind     string  `json:"kind" binding:"required,oneof=open replace"`
	StartsAt string  `json:"startsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EndsAt   string  `json:"endsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	Note     *string `json:"note" binding:"omitnil,max=500"`
}

type overrideListResponse struct {
	Timezone string             `json:"timezone"`
	Items    []overrideResponse `json:"items"`
}

type overrideResponse struct {
	ID        pgtype.UUID `json:"id"`
	OnDate    string      `json:"onDate"`
	Kind      string      `json:"kind"`
	StartsAt  time.Time   `json:"startsAt"`
	EndsAt    time.Time   `json:"endsAt"`
	Note      *string     `json:"note,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

func (h *Handler) listOverrides(c *gin.Context) {
	var query listOverridesQuery
	if !web.BindQuery(c, &query) {
		return
	}
	ctx := c.Request.Context()

	q := db.New(h.Pool)
	timezone, loc, err := PracticeZone(ctx, q)
	if err != nil {
		web.Error(c, err)
		return
	}
	first, last, err := DateWindow(h.Now(), loc, dateOf(query.From), dateOf(query.To))
	if err != nil {
		web.Error(c, err)
		return
	}
	overrides, err := ListOverrides(ctx, q, first, last)
	if err != nil {
		web.Error(c, err)
		return
	}

	response := overrideListResponse{
		Timezone: timezone,
		Items:    make([]overrideResponse, len(overrides)),
	}
	for i, override := range overrides {
		response.Items[i] = newOverrideResponse(override)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) createOverride(c *gin.Context) {
	var body overrideBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	override, err := CreateOverride(ctx, h.Pool, newOverride(body), currentUserID(c))
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_override_created", override.ID)
	c.JSON(http.StatusCreated, newOverrideResponse(override))
}

func (h *Handler) updateOverride(c *gin.Context) {
	overrideID, ok := web.PathUUID(c, "overrideId")
	if !ok {
		return
	}
	var body overrideBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	override, err := UpdateOverride(ctx, h.Pool, overrideID, newOverride(body), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_override_updated", override.ID)
	c.JSON(http.StatusOK, newOverrideResponse(override))
}

func (h *Handler) deleteOverride(c *gin.Context) {
	overrideID, ok := web.PathUUID(c, "overrideId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := DeleteOverride(ctx, h.Pool, overrideID)
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_override_deleted", overrideID)
	c.Status(http.StatusNoContent)
}

func newOverrideResponse(override db.AvailabilityOverride) overrideResponse {
	period := PeriodOf(override.Period)
	return overrideResponse{
		ID:        override.ID,
		OnDate:    override.OnDate.Time.Format(time.DateOnly),
		Kind:      override.Kind,
		StartsAt:  period.Start.UTC(),
		EndsAt:    period.End.UTC(),
		Note:      stringOf(override.Note),
		CreatedAt: override.CreatedAt.UTC(),
	}
}

func newOverride(body overrideBody) Override {
	return Override{
		OnDate: *dateOf(body.OnDate),
		Kind:   body.Kind,
		Period: periodOf(body.StartsAt, body.EndsAt),
		Note:   textOf(body.Note),
	}
}
