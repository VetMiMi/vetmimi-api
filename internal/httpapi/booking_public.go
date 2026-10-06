package httpapi

import (
	"context"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// ListPublicBookableServices lists what a visitor can book, empty while
// public booking is paused.
func (s *server) ListPublicBookableServices(ctx context.Context, req gen.ListPublicBookableServicesRequestObject) (gen.ListPublicBookableServicesResponseObject, error) {
	locale := gen.En
	if req.Params.Locale != nil {
		locale = *req.Params.Locale
	}
	list, err := booking.ListPublicServices(ctx, db.New(s.Pool), string(locale))
	if err != nil {
		return nil, err
	}
	items := make([]gen.PublicBookableService, len(list.Items))
	for i, svc := range list.Items {
		items[i] = gen.PublicBookableService{
			Slug:            svc.Slug,
			Name:            svc.Name,
			Description:     nonEmpty(svc.Description),
			BookingAction:   gen.PublicBookableServiceBookingAction(svc.BookingAction),
			DurationMinutes: svc.DurationMinutes,
			Formats:         make([]gen.Format, len(svc.Formats)),
			FeeText:         nonEmpty(svc.FeeText),
		}
		for j, f := range svc.Formats {
			items[i].Formats[j] = gen.Format(f)
		}
	}
	return gen.ListPublicBookableServices200JSONResponse{
		BookingEnabled: list.BookingEnabled,
		BookingMode:    gen.PublicBookableServiceListBookingMode(list.BookingMode),
		Timezone:       list.Timezone,
		Items:          items,
	}, nil
}

// GetPublicAvailability answers the free slots for a service: times only,
// never appointments or the reasons time is blocked.
func (s *server) GetPublicAvailability(ctx context.Context, req gen.GetPublicAvailabilityRequestObject) (gen.GetPublicAvailabilityResponseObject, error) {
	p := req.Params
	a, err := booking.PublicAvailability(ctx, db.New(s.Pool), p.Service, p.From.Time, p.To.Time, s.Now())
	if err != nil {
		return nil, err
	}
	return gen.GetPublicAvailability200JSONResponse{
		Service:  p.Service,
		Timezone: a.Timezone,
		From:     openapi_types.Date{Time: a.First},
		To:       openapi_types.Date{Time: a.Last},
		Days:     slotDaysView(a.Days),
	}, nil
}

func slotDaysView(days []booking.SlotDay) []gen.SlotDay {
	out := make([]gen.SlotDay, len(days))
	for i, d := range days {
		out[i] = gen.SlotDay{Date: openapi_types.Date{Time: d.Date}, Slots: make([]gen.Slot, len(d.Slots))}
		for j, slot := range d.Slots {
			out[i].Slots[j] = gen.Slot{StartsAt: slot.Start.UTC(), EndsAt: slot.End.UTC()}
		}
	}
	return out
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
