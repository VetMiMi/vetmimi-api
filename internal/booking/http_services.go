package booking

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/web"
)

// localized is text per locale, en or my; English is checked by the rules, not here.
// A map, not a struct, so a wrong locale is reported at its own path, such as /name/fr.
type localized map[string]string

type serviceInput struct {
	Slug                string    `json:"slug" binding:"required,slug,max=120"`
	Name                localized `json:"name" binding:"required,dive,keys,oneof=en my,endkeys,max=20000"`
	Description         localized `json:"description" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	BookingAction       string    `json:"bookingAction" binding:"required,oneof=book request enquiry_only not_bookable"`
	DurationMinutes     *int32    `json:"durationMinutes" binding:"omitnil,min=5,max=480"`
	BufferBeforeMinutes int32     `json:"bufferBeforeMinutes" binding:"min=0,max=240"`
	BufferAfterMinutes  int32     `json:"bufferAfterMinutes" binding:"min=0,max=240"`
	Formats             []string  `json:"formats" binding:"omitnil,min=1,unique,dive,oneof=online in_person"`
	FeeText             localized `json:"feeText" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	PreparationText     localized `json:"preparationText" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	SortOrder           int32     `json:"sortOrder"`
}

// servicePatch changes only the fields it names.
type servicePatch struct {
	Version             *int32    `json:"version" binding:"required,min=1"`
	Slug                *string   `json:"slug" binding:"omitnil,slug,max=120"`
	Name                localized `json:"name" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	Description         localized `json:"description" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	BookingAction       *string   `json:"bookingAction" binding:"omitnil,oneof=book request enquiry_only not_bookable"`
	DurationMinutes     *int32    `json:"durationMinutes" binding:"omitnil,min=5,max=480"`
	BufferBeforeMinutes *int32    `json:"bufferBeforeMinutes" binding:"omitnil,min=0,max=240"`
	BufferAfterMinutes  *int32    `json:"bufferAfterMinutes" binding:"omitnil,min=0,max=240"`
	Formats             []string  `json:"formats" binding:"omitnil,min=1,unique,dive,oneof=online in_person"`
	FeeText             localized `json:"feeText" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	PreparationText     localized `json:"preparationText" binding:"omitnil,dive,keys,oneof=en my,endkeys,max=20000"`
	SortOrder           *int32    `json:"sortOrder"`
	State               *string   `json:"state" binding:"omitnil,oneof=active paused archived"`
}

type versionInput struct {
	Version *int32 `json:"version" binding:"required,min=1"`
}

type serviceJSON struct {
	ID                  pgtype.UUID     `json:"id"`
	Slug                string          `json:"slug"`
	Name                json.RawMessage `json:"name"`
	Description         json.RawMessage `json:"description,omitempty"`
	BookingAction       string          `json:"bookingAction"`
	State               string          `json:"state"`
	DurationMinutes     *int32          `json:"durationMinutes,omitempty"`
	BufferBeforeMinutes int32           `json:"bufferBeforeMinutes"`
	BufferAfterMinutes  int32           `json:"bufferAfterMinutes"`
	Formats             []string        `json:"formats"`
	FeeText             json.RawMessage `json:"feeText,omitempty"`
	PreparationText     json.RawMessage `json:"preparationText,omitempty"`
	SortOrder           int32           `json:"sortOrder"`
	Version             int32           `json:"version"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

func (h *Handler) listServices(c *gin.Context) {
	rows, err := ListServices(c.Request.Context(), db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}
	items := make([]serviceJSON, len(rows))
	for i, r := range rows {
		items[i] = serviceOf(r)
	}
	c.JSON(http.StatusOK, gin.H{"items": items})
}

func (h *Handler) createService(c *gin.Context) {
	var in serviceInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	created, err := CreateService(ctx, db.New(h.Pool), db.CreateServiceParams{
		Slug:                in.Slug,
		Name:                localizedJSON(in.Name),
		Description:         localizedJSON(in.Description),
		BookingAction:       in.BookingAction,
		DurationMinutes:     optionalInt4(in.DurationMinutes),
		BufferBeforeMinutes: in.BufferBeforeMinutes,
		BufferAfterMinutes:  in.BufferAfterMinutes,
		Formats:             in.Formats,
		FeeText:             localizedJSON(in.FeeText),
		PreparationText:     localizedJSON(in.PreparationText),
		SortOrder:           in.SortOrder,
	})
	if err != nil {
		web.Error(c, err)
		return
	}
	logService(ctx, "service_created", created)
	c.JSON(http.StatusCreated, serviceOf(created))
}

func (h *Handler) getService(c *gin.Context) {
	id, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	svc, err := GetService(c.Request.Context(), db.New(h.Pool), id)
	if err != nil {
		web.Error(c, err)
		return
	}
	c.JSON(http.StatusOK, serviceOf(svc))
}

func (h *Handler) updateService(c *gin.Context) {
	id, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	var in servicePatch
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	updated, err := UpdateService(ctx, db.New(h.Pool), id, *in.Version, h.Now(), in.apply)
	if err != nil {
		web.Error(c, err)
		return
	}
	logService(ctx, "service_updated", updated)
	c.JSON(http.StatusOK, serviceOf(updated))
}

func (in servicePatch) apply(p *db.UpdateServiceParams) {
	if in.Slug != nil {
		p.Slug = *in.Slug
	}
	if in.Name != nil {
		p.Name = localizedJSON(in.Name)
	}
	if in.Description != nil {
		p.Description = localizedJSON(in.Description)
	}
	if in.BookingAction != nil {
		p.BookingAction = *in.BookingAction
	}
	if in.State != nil {
		p.State = *in.State
	}
	if in.DurationMinutes != nil {
		p.DurationMinutes = optionalInt4(in.DurationMinutes)
	}
	if in.BufferBeforeMinutes != nil {
		p.BufferBeforeMinutes = *in.BufferBeforeMinutes
	}
	if in.BufferAfterMinutes != nil {
		p.BufferAfterMinutes = *in.BufferAfterMinutes
	}
	if in.Formats != nil {
		p.Formats = in.Formats
	}
	if in.FeeText != nil {
		p.FeeText = localizedJSON(in.FeeText)
	}
	if in.PreparationText != nil {
		p.PreparationText = localizedJSON(in.PreparationText)
	}
	if in.SortOrder != nil {
		p.SortOrder = *in.SortOrder
	}
}

func (h *Handler) deleteService(c *gin.Context) {
	id, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	ctx := c.Request.Context()
	if err := DeleteService(ctx, db.New(h.Pool), id); err != nil {
		web.Error(c, err)
		return
	}
	slog.InfoContext(ctx, "service_deleted", "request_id", web.RequestID(ctx), "service_id", id.String())
	c.Status(http.StatusNoContent)
}

// pauseService stops new bookings; the service page stays published.
func (h *Handler) pauseService(c *gin.Context) {
	h.setServiceState(c, "paused", "service_paused")
}

func (h *Handler) resumeService(c *gin.Context) {
	h.setServiceState(c, "active", "service_resumed")
}

func (h *Handler) setServiceState(c *gin.Context, state, event string) {
	id, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	var in versionInput
	if !web.BindJSON(c, &in) {
		return
	}
	ctx := c.Request.Context()
	svc, err := SetServiceState(ctx, db.New(h.Pool), id, *in.Version, h.Now(), state)
	if err != nil {
		web.Error(c, err)
		return
	}
	logService(ctx, event, svc)
	c.JSON(http.StatusOK, serviceOf(svc))
}

func logService(ctx context.Context, event string, svc db.Service) {
	slog.InfoContext(ctx, event, "request_id", web.RequestID(ctx), "service_id", svc.ID.String(),
		"state", svc.State, "version", svc.Version)
}

func serviceOf(r db.Service) serviceJSON {
	s := serviceJSON{
		ID:                  r.ID,
		Slug:                r.Slug,
		Name:                r.Name,
		Description:         r.Description,
		BookingAction:       r.BookingAction,
		State:               r.State,
		BufferBeforeMinutes: r.BufferBeforeMinutes,
		BufferAfterMinutes:  r.BufferAfterMinutes,
		Formats:             r.Formats,
		FeeText:             r.FeeText,
		PreparationText:     r.PreparationText,
		SortOrder:           r.SortOrder,
		Version:             r.Version,
		CreatedAt:           r.CreatedAt.UTC(),
		UpdatedAt:           r.UpdatedAt.UTC(),
	}
	if r.DurationMinutes.Valid {
		s.DurationMinutes = &r.DurationMinutes.Int32
	}
	return s
}

// localizedJSON stores text per locale; nil stays NULL.
func localizedJSON(t localized) json.RawMessage {
	if t == nil {
		return nil
	}
	b, _ := json.Marshal(t) // a map of strings always marshals
	return b
}

func optionalInt4(n *int32) pgtype.Int4 {
	if n == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *n, Valid: true}
}
