package httpapi

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// ListPublicBookableServices is empty while public booking is paused.
func (s *server) ListPublicBookableServices(ctx context.Context, req gen.ListPublicBookableServicesRequestObject) (gen.ListPublicBookableServicesResponseObject, error) {
	locale := cmp.Or(deref(req.Params.Locale), gen.LocaleEn)
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

// GetPublicAvailability answers times only, never appointments or why time is blocked.
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

// CreatePublicAppointment stores a visitor's request, then enqueues its emails
// and hold expiry. The receipt never carries the id or the management token.
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
		VisitorPhone:        deref(b.Visitor.Phone),
		VisitorNote:         deref(b.Visitor.Note),
		PrivacyAcknowledged: bool(b.PrivacyAcknowledged),
		PolicyAcknowledged:  bool(b.PolicyAcknowledged),
	}
	res, err := booking.RequestAppointment(ctx, s.Pool, s.SigningSecret, r, s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, res.Tasks...)
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

// replayedReceipt marks a stored receipt answered again.
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

// GetManagedAppointment answers the same 404 for an unknown and an expired link.
func (s *server) GetManagedAppointment(ctx context.Context, req gen.GetManagedAppointmentRequestObject) (gen.GetManagedAppointmentResponseObject, error) {
	m, err := booking.GetManaged(ctx, db.New(s.Pool), req.Token, s.Now())
	if err != nil {
		return nil, err
	}
	return gen.GetManagedAppointment200JSONResponse(managedView(m)), nil
}

func (s *server) CancelManagedAppointment(ctx context.Context, req gen.CancelManagedAppointmentRequestObject) (gen.CancelManagedAppointmentResponseObject, error) {
	var message string
	if req.Body != nil {
		message = deref(req.Body.Message)
	}
	now := s.Now()
	changed, err := booking.CancelByClient(ctx, s.Pool, req.Token, message, now)
	if err != nil {
		return nil, err
	}
	m, err := s.afterManagedChange(ctx, "appointment_cancelled_by_client", req.Token, changed, now)
	if err != nil {
		return nil, err
	}
	return gen.CancelManagedAppointment200JSONResponse(managedView(m)), nil
}

// RequestManagedReschedule tells Daw Mi; the appointment keeps its time.
func (s *server) RequestManagedReschedule(ctx context.Context, req gen.RequestManagedRescheduleRequestObject) (gen.RequestManagedRescheduleResponseObject, error) {
	var preferred []time.Time
	var message string
	if req.Body != nil {
		preferred = deref(req.Body.PreferredTimes)
		message = deref(req.Body.Message)
	}
	now := s.Now()
	changed, err := booking.RequestReschedule(ctx, s.Pool, req.Token, preferred, message, now)
	if err != nil {
		return nil, err
	}
	m, err := s.afterManagedChange(ctx, "reschedule_requested", req.Token, changed, now)
	if err != nil {
		return nil, err
	}
	return gen.RequestManagedReschedule202JSONResponse(managedView(m)), nil
}

// afterManagedChange applies a committed change and reads the appointment
// again. The log names the event only: the token is as good as a password.
func (s *server) afterManagedChange(ctx context.Context, msg, token string, changed booking.Changed,
	now time.Time) (booking.Managed, error) {
	s.afterCommit(ctx, changed)
	s.Log.InfoContext(ctx, msg, "request_id", RequestID(ctx))
	return booking.GetManaged(ctx, db.New(s.Pool), token, now)
}

func managedView(m booking.Managed) gen.ManagedAppointment {
	return gen.ManagedAppointment{
		Reference:               m.Reference,
		Status:                  gen.AppointmentStatus(m.Status),
		Service:                 gen.PublicServiceRef{Slug: m.ServiceSlug, Name: m.ServiceName},
		StartsAt:                m.StartsAt,
		EndsAt:                  m.EndsAt,
		DurationMinutes:         m.DurationMinutes,
		Timezone:                m.Timezone,
		Format:                  gen.Format(m.Format),
		CanCancel:               m.CanCancel,
		CanRequestReschedule:    m.CanRequestReschedule,
		CancellationNoticeHours: m.CancellationNoticeHours,
		LateIfCancelledNow:      m.LateIfCancelledNow,
		RescheduleRequested:     m.RescheduleRequested,
	}
}
