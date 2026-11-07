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

type createServiceBody struct {
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

// updateServiceBody changes only the fields it names.
type updateServiceBody struct {
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

type versionBody struct {
	Version *int32 `json:"version" binding:"required,min=1"`
}

type serviceListResponse struct {
	Items []serviceResponse `json:"items"`
}

type serviceResponse struct {
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
	ctx := c.Request.Context()

	services, err := ListServices(ctx, db.New(h.Pool))
	if err != nil {
		web.Error(c, err)
		return
	}

	response := serviceListResponse{
		Items: make([]serviceResponse, len(services)),
	}
	for i, service := range services {
		response.Items[i] = newServiceResponse(service)
	}
	c.JSON(http.StatusOK, response)
}

func (h *Handler) createService(c *gin.Context) {
	var body createServiceBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	params := db.CreateServiceParams{
		Slug:                body.Slug,
		Name:                localizedJSON(body.Name),
		Description:         localizedJSON(body.Description),
		BookingAction:       body.BookingAction,
		DurationMinutes:     optionalInt4(body.DurationMinutes),
		BufferBeforeMinutes: body.BufferBeforeMinutes,
		BufferAfterMinutes:  body.BufferAfterMinutes,
		Formats:             body.Formats,
		FeeText:             localizedJSON(body.FeeText),
		PreparationText:     localizedJSON(body.PreparationText),
		SortOrder:           body.SortOrder,
	}
	service, err := CreateService(ctx, db.New(h.Pool), params)
	if err != nil {
		web.Error(c, err)
		return
	}

	logService(ctx, "service_created", service)
	c.JSON(http.StatusCreated, newServiceResponse(service))
}

func (h *Handler) getService(c *gin.Context) {
	serviceID, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	service, err := GetService(ctx, db.New(h.Pool), serviceID)
	if err != nil {
		web.Error(c, err)
		return
	}

	c.JSON(http.StatusOK, newServiceResponse(service))
}

func (h *Handler) updateService(c *gin.Context) {
	serviceID, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	var body updateServiceBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	service, err := UpdateService(ctx, db.New(h.Pool), serviceID, *body.Version, h.Now(), body.apply)
	if err != nil {
		web.Error(c, err)
		return
	}

	logService(ctx, "service_updated", service)
	c.JSON(http.StatusOK, newServiceResponse(service))
}

func (h *Handler) deleteService(c *gin.Context) {
	serviceID, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	ctx := c.Request.Context()

	err := DeleteService(ctx, db.New(h.Pool), serviceID)
	if err != nil {
		web.Error(c, err)
		return
	}

	slog.InfoContext(ctx, "service_deleted", "request_id", web.RequestID(ctx), "service_id", serviceID.String())
	c.Status(http.StatusNoContent)
}

// pauseService stops new bookings; the service page stays published.
func (h *Handler) pauseService(c *gin.Context) {
	serviceID, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	var body versionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	service, err := SetServiceState(ctx, db.New(h.Pool), serviceID, *body.Version, h.Now(), "paused")
	if err != nil {
		web.Error(c, err)
		return
	}

	logService(ctx, "service_paused", service)
	c.JSON(http.StatusOK, newServiceResponse(service))
}

func (h *Handler) resumeService(c *gin.Context) {
	serviceID, ok := web.PathUUID(c, "serviceId")
	if !ok {
		return
	}
	var body versionBody
	if !web.BindJSON(c, &body) {
		return
	}
	ctx := c.Request.Context()

	service, err := SetServiceState(ctx, db.New(h.Pool), serviceID, *body.Version, h.Now(), "active")
	if err != nil {
		web.Error(c, err)
		return
	}

	logService(ctx, "service_resumed", service)
	c.JSON(http.StatusOK, newServiceResponse(service))
}

func newServiceResponse(service db.Service) serviceResponse {
	response := serviceResponse{
		ID:                  service.ID,
		Slug:                service.Slug,
		Name:                service.Name,
		Description:         service.Description,
		BookingAction:       service.BookingAction,
		State:               service.State,
		BufferBeforeMinutes: service.BufferBeforeMinutes,
		BufferAfterMinutes:  service.BufferAfterMinutes,
		Formats:             service.Formats,
		FeeText:             service.FeeText,
		PreparationText:     service.PreparationText,
		SortOrder:           service.SortOrder,
		Version:             service.Version,
		CreatedAt:           service.CreatedAt.UTC(),
		UpdatedAt:           service.UpdatedAt.UTC(),
	}
	if service.DurationMinutes.Valid {
		response.DurationMinutes = &service.DurationMinutes.Int32
	}
	return response
}

// apply copies the fields the admin sent onto the saved service.
func (body updateServiceBody) apply(params *db.UpdateServiceParams) {
	if body.Slug != nil {
		params.Slug = *body.Slug
	}
	if body.Name != nil {
		params.Name = localizedJSON(body.Name)
	}
	if body.Description != nil {
		params.Description = localizedJSON(body.Description)
	}
	if body.BookingAction != nil {
		params.BookingAction = *body.BookingAction
	}
	if body.State != nil {
		params.State = *body.State
	}
	if body.DurationMinutes != nil {
		params.DurationMinutes = optionalInt4(body.DurationMinutes)
	}
	if body.BufferBeforeMinutes != nil {
		params.BufferBeforeMinutes = *body.BufferBeforeMinutes
	}
	if body.BufferAfterMinutes != nil {
		params.BufferAfterMinutes = *body.BufferAfterMinutes
	}
	if body.Formats != nil {
		params.Formats = body.Formats
	}
	if body.FeeText != nil {
		params.FeeText = localizedJSON(body.FeeText)
	}
	if body.PreparationText != nil {
		params.PreparationText = localizedJSON(body.PreparationText)
	}
	if body.SortOrder != nil {
		params.SortOrder = *body.SortOrder
	}
}

// logService logs ids and state only.
func logService(ctx context.Context, event string, service db.Service) {
	slog.InfoContext(ctx, event,
		"request_id", web.RequestID(ctx),
		"service_id", service.ID.String(),
		"state", service.State,
		"version", service.Version,
	)
}

// localizedJSON stores text per locale; nil stays NULL.
func localizedJSON(text localized) json.RawMessage {
	if text == nil {
		return nil
	}
	encoded, _ := json.Marshal(text) // a map of strings always marshals
	return encoded
}

func optionalInt4(n *int32) pgtype.Int4 {
	if n == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: *n, Valid: true}
}
