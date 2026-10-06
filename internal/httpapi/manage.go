package httpapi

import (
	"context"
	"time"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// GetManagedAppointment shows the appointment a management link names. An
// unknown and an expired link answer the same 404.
func (s *server) GetManagedAppointment(ctx context.Context, req gen.GetManagedAppointmentRequestObject) (gen.GetManagedAppointmentResponseObject, error) {
	m, err := booking.GetManaged(ctx, db.New(s.Pool), req.Token, s.Now())
	if err != nil {
		return nil, err
	}
	return gen.GetManagedAppointment200JSONResponse(managedView(m)), nil
}

// CancelManagedAppointment cancels as the visitor and answers with the
// appointment as the link now shows it.
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

// RequestManagedReschedule records the visitor's wish to move and tells Daw
// Mi; the appointment keeps its time.
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

// afterManagedChange hands the queue what the committed change asks of it,
// closes the sockets of a video room it ended, and reads the appointment
// again. The log names the event only: the token
// is as good as a password, and the request log has only the route pattern.
func (s *server) afterManagedChange(ctx context.Context, msg, token string, changed booking.Changed,
	now time.Time) (booking.Managed, error) {
	if s.Queue != nil {
		s.Queue.Remove(changed.Remove...)
		s.Queue.Enqueue(ctx, changed.Tasks...)
	}
	s.Hub.EndRoom(changed.EndedRoom)
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
