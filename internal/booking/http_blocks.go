package booking

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// listBlocksQuery is optional local dates; the rules default to the next 62 days.
type listBlocksQuery struct {
	From string `form:"from" binding:"omitempty,datetime=2006-01-02"`
	To   string `form:"to" binding:"omitempty,datetime=2006-01-02"`
}

// Times are bound as strings so that a bad one is reported by its field name.
type blockBody struct {
	StartsAt string  `json:"startsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	EndsAt   string  `json:"endsAt" binding:"required,datetime=2006-01-02T15:04:05Z07:00"`
	AllDay   bool    `json:"allDay"`
	Reason   *string `json:"reason" binding:"omitnil,max=500"`
}

type blockListResponse struct {
	Timezone string          `json:"timezone"`
	Items    []blockResponse `json:"items"`
}

type blockResponse struct {
	ID        pgtype.UUID `json:"id"`
	StartsAt  time.Time   `json:"startsAt"`
	EndsAt    time.Time   `json:"endsAt"`
	AllDay    bool        `json:"allDay"`
	Reason    *string     `json:"reason,omitempty"`
	CreatedAt time.Time   `json:"createdAt"`
}

type savedBlockResponse struct {
	Block     blockResponse                `json:"block"`
	Conflicts []appointmentSummaryResponse `json:"conflicts"`
}

func (h *Handler) listBlocks(c *gin.Context) {
	var query listBlocksQuery
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
	blocks, err := ListBlocks(ctx, q, LocalDays(first, last, loc))
	if err != nil {
		web.Error(c, err)
		return
	}

	response := blockListResponse{
		Timezone: timezone,
		Items:    make([]blockResponse, len(blocks)),
	}
	for i, block := range blocks {
		response.Items[i] = newBlockResponse(block)
	}
	c.JSON(http.StatusOK, response)
}

// createBlock answers the appointments the block overlaps, which it leaves alone.
func (h *Handler) createBlock(c *gin.Context) {
	var body blockBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	saved, err := CreateBlock(ctx, h.Pool, newBlock(body), currentUserID(c))
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_block_created", saved.Block.ID, "conflicts", len(saved.Conflicts))
	c.JSON(http.StatusCreated, newSavedBlockResponse(saved))
}

func (h *Handler) updateBlock(c *gin.Context) {
	blockID, ok := web.PathUUID(c, "blockId")
	if !ok {
		return
	}
	var body blockBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	saved, err := UpdateBlock(ctx, h.Pool, blockID, newBlock(body), h.Now())
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_block_updated", saved.Block.ID, "conflicts", len(saved.Conflicts))
	c.JSON(http.StatusOK, newSavedBlockResponse(saved))
}

func (h *Handler) deleteBlock(c *gin.Context) {
	blockID, ok := web.PathUUID(c, "blockId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := DeleteBlock(ctx, h.Pool, blockID)
	if err != nil {
		web.Error(c, err)
		return
	}

	logAvailability(ctx, "availability_block_deleted", blockID)
	c.Status(http.StatusNoContent)
}

func newBlockResponse(block db.AvailabilityBlock) blockResponse {
	period := PeriodOf(block.Period)
	return blockResponse{
		ID:        block.ID,
		StartsAt:  period.Start.UTC(),
		EndsAt:    period.End.UTC(),
		AllDay:    block.AllDay,
		Reason:    stringOf(block.Reason),
		CreatedAt: block.CreatedAt.UTC(),
	}
}

// newSavedBlockResponse lists each conflict as an appointment summary: the visitor's name, never their email or notes.
func newSavedBlockResponse(saved SavedBlock) savedBlockResponse {
	response := savedBlockResponse{
		Block:     newBlockResponse(saved.Block),
		Conflicts: make([]appointmentSummaryResponse, len(saved.Conflicts)),
	}
	for i, row := range saved.Conflicts {
		response.Conflicts[i] = appointmentSummaryResponse{
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
	return response
}

func newBlock(body blockBody) Block {
	return Block{
		Period: periodOf(body.StartsAt, body.EndsAt),
		AllDay: body.AllDay,
		Reason: textOf(body.Reason),
	}
}
