package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

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

// CreatePublicAppointment stores a visitor's request and, only after it has
// committed, enqueues its emails and hold expiry; a failed enqueue is logged
// by the queue and left to the sweepers. The receipt never carries the id or
// the management token.
func (s *server) CreatePublicAppointment(ctx context.Context, req gen.CreatePublicAppointmentRequestObject) (gen.CreatePublicAppointmentResponseObject, error) {
	b := req.Body
	// The key's hash covers the body as decoded and encoded again, so a
	// retry that differs only in spacing or key order is the same request.
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	r := booking.Request{
		IdempotencyKey:      req.Params.IdempotencyKey.String(),
		Body:                raw,
		Service:             b.Service,
		StartsAt:            b.StartsAt,
		Format:              string(b.Format),
		Locale:              string(b.Locale),
		VisitorName:         b.Visitor.Name,
		VisitorEmail:        string(b.Visitor.Email),
		PrivacyAcknowledged: bool(b.PrivacyAcknowledged),
		PolicyAcknowledged:  bool(b.PolicyAcknowledged),
	}
	if b.Visitor.Phone != nil {
		r.VisitorPhone = *b.Visitor.Phone
	}
	if b.Visitor.Note != nil {
		r.VisitorNote = *b.Visitor.Note
	}
	res, err := booking.RequestAppointment(ctx, s.Pool, s.SigningSecret, r, s.Now())
	if err != nil {
		return nil, err
	}
	if s.Queue != nil {
		s.Queue.Enqueue(ctx, res.Tasks...)
	}
	s.Log.InfoContext(ctx, "appointment requested", "request_id", RequestID(ctx),
		"appointment_id", res.AppointmentID.String(), "replayed", res.Replayed)

	rc := res.Receipt
	receipt := gen.CreatePublicAppointment201JSONResponse{
		Reference:       rc.Reference,
		Status:          gen.AppointmentRequestReceiptStatus(rc.Status),
		Service:         gen.PublicServiceRef{Slug: rc.Service.Slug, Name: rc.Service.Name},
		StartsAt:        rc.StartsAt.UTC(),
		EndsAt:          rc.EndsAt.UTC(),
		DurationMinutes: rc.DurationMinutes,
		Timezone:        rc.Timezone,
		Format:          gen.Format(rc.Format),
	}
	if res.Replayed {
		return replayedReceipt{receipt}, nil
	}
	return receipt, nil
}

// replayedReceipt is a stored receipt answered again, marked so the site
// can tell a replay from a new request.
type replayedReceipt struct {
	gen.CreatePublicAppointment201JSONResponse
}

func (r replayedReceipt) VisitCreatePublicAppointmentResponse(w http.ResponseWriter) error {
	w.Header().Set("Idempotent-Replayed", "true")
	return r.CreatePublicAppointment201JSONResponse.VisitCreatePublicAppointmentResponse(w)
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
