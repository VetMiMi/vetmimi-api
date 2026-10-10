package httpapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func (s *server) ListServices(ctx context.Context, _ gen.ListServicesRequestObject) (gen.ListServicesResponseObject, error) {
	rows, err := booking.ListServices(ctx, db.New(s.Pool))
	if err != nil {
		return nil, err
	}
	items := make([]gen.Service, len(rows))
	for i, r := range rows {
		items[i] = serviceView(r)
	}
	return gen.ListServices200JSONResponse{Items: items}, nil
}

func (s *server) CreateService(ctx context.Context, req gen.CreateServiceRequestObject) (gen.CreateServiceResponseObject, error) {
	b := req.Body
	p := db.CreateServiceParams{
		Slug:                b.Slug,
		Name:                localizedJSON(&b.Name),
		Description:         localizedJSON(b.Description),
		BookingAction:       string(b.BookingAction),
		DurationMinutes:     optionalInt4(b.DurationMinutes),
		BufferBeforeMinutes: int32(deref(b.BufferBeforeMinutes)),
		BufferAfterMinutes:  int32(deref(b.BufferAfterMinutes)),
		FeeText:             localizedJSON(b.FeeText),
		PreparationText:     localizedJSON(b.PreparationText),
		SortOrder:           int32(deref(b.SortOrder)),
	}
	if b.Formats != nil {
		p.Formats = formats(*b.Formats)
	}
	created, err := booking.CreateService(ctx, db.New(s.Pool), p)
	if err != nil {
		return nil, err
	}
	s.logService(ctx, "service_created", created)
	return gen.CreateService201JSONResponse(serviceView(created)), nil
}

func (s *server) GetService(ctx context.Context, req gen.GetServiceRequestObject) (gen.GetServiceResponseObject, error) {
	svc, err := booking.GetService(ctx, db.New(s.Pool), uuid(req.ServiceId))
	if err != nil {
		return nil, err
	}
	return gen.GetService200JSONResponse(serviceView(svc)), nil
}

func (s *server) UpdateService(ctx context.Context, req gen.UpdateServiceRequestObject) (gen.UpdateServiceResponseObject, error) {
	b := req.Body
	updated, err := booking.UpdateService(ctx, db.New(s.Pool), uuid(req.ServiceId), int32(b.Version), s.Now(),
		func(p *db.UpdateServiceParams) { applyServicePatch(p, b) })
	if err != nil {
		return nil, err
	}
	s.logService(ctx, "service_updated", updated)
	return gen.UpdateService200JSONResponse(serviceView(updated)), nil
}

// applyServicePatch changes only the fields the patch names.
func applyServicePatch(p *db.UpdateServiceParams, b *gen.ServicePatch) {
	if b.Slug != nil {
		p.Slug = *b.Slug
	}
	if b.Name != nil {
		p.Name = localizedJSON(b.Name)
	}
	if b.Description != nil {
		p.Description = localizedJSON(b.Description)
	}
	if b.BookingAction != nil {
		p.BookingAction = string(*b.BookingAction)
	}
	if b.State != nil {
		p.State = string(*b.State)
	}
	if b.DurationMinutes != nil {
		p.DurationMinutes = optionalInt4(b.DurationMinutes)
	}
	if b.BufferBeforeMinutes != nil {
		p.BufferBeforeMinutes = int32(*b.BufferBeforeMinutes)
	}
	if b.BufferAfterMinutes != nil {
		p.BufferAfterMinutes = int32(*b.BufferAfterMinutes)
	}
	if b.Formats != nil {
		p.Formats = formats(*b.Formats)
	}
	if b.FeeText != nil {
		p.FeeText = localizedJSON(b.FeeText)
	}
	if b.PreparationText != nil {
		p.PreparationText = localizedJSON(b.PreparationText)
	}
	if b.SortOrder != nil {
		p.SortOrder = int32(*b.SortOrder)
	}
}

func (s *server) DeleteService(ctx context.Context, req gen.DeleteServiceRequestObject) (gen.DeleteServiceResponseObject, error) {
	if err := booking.DeleteService(ctx, db.New(s.Pool), uuid(req.ServiceId)); err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "service_deleted", "request_id", RequestID(ctx), "service_id", req.ServiceId.String())
	return gen.DeleteService204Response{}, nil
}

// PauseService stops new bookings; the service page stays published.
func (s *server) PauseService(ctx context.Context, req gen.PauseServiceRequestObject) (gen.PauseServiceResponseObject, error) {
	svc, err := booking.SetServiceState(ctx, db.New(s.Pool), uuid(req.ServiceId), int32(req.Body.Version), s.Now(), "paused")
	if err != nil {
		return nil, err
	}
	s.logService(ctx, "service_paused", svc)
	return gen.PauseService200JSONResponse(serviceView(svc)), nil
}

func (s *server) ResumeService(ctx context.Context, req gen.ResumeServiceRequestObject) (gen.ResumeServiceResponseObject, error) {
	svc, err := booking.SetServiceState(ctx, db.New(s.Pool), uuid(req.ServiceId), int32(req.Body.Version), s.Now(), "active")
	if err != nil {
		return nil, err
	}
	s.logService(ctx, "service_resumed", svc)
	return gen.ResumeService200JSONResponse(serviceView(svc)), nil
}

func (s *server) logService(ctx context.Context, msg string, svc db.Service) {
	s.Log.InfoContext(ctx, msg, "request_id", RequestID(ctx), "service_id", svc.ID.String(),
		"state", svc.State, "version", svc.Version)
}

func serviceView(r db.Service) gen.Service {
	v := gen.Service{
		Id:                  openapi_types.UUID(r.ID.Bytes),
		Slug:                r.Slug,
		Name:                deref(localizedView(r.Name)),
		Description:         localizedView(r.Description),
		BookingAction:       gen.BookingAction(r.BookingAction),
		State:               gen.ServiceState(r.State),
		BufferBeforeMinutes: int(r.BufferBeforeMinutes),
		BufferAfterMinutes:  int(r.BufferAfterMinutes),
		Formats:             make([]gen.Format, len(r.Formats)),
		FeeText:             localizedView(r.FeeText),
		PreparationText:     localizedView(r.PreparationText),
		SortOrder:           int(r.SortOrder),
		Version:             int(r.Version),
		CreatedAt:           r.CreatedAt.UTC(),
		UpdatedAt:           r.UpdatedAt.UTC(),
	}
	if r.DurationMinutes.Valid {
		v.DurationMinutes = new(int(r.DurationMinutes.Int32))
	}
	for i, f := range r.Formats {
		v.Formats[i] = gen.Format(f)
	}
	return v
}

func formats(fs []gen.Format) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = string(f)
	}
	return out
}
