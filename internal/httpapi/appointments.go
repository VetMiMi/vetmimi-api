package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/auth"
	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// ListAppointments lists appointments by view, filters and search.
func (s *server) ListAppointments(ctx context.Context, req gen.ListAppointmentsRequestObject) (gen.ListAppointmentsResponseObject, error) {
	p := req.Params
	f := booking.Filter{
		View:   booking.View(deref((*string)(p.View))),
		From:   deref(p.From),
		To:     deref(p.To),
		Format: string(deref(p.Format)),
		Search: deref(p.Q),
		Cursor: deref(p.Cursor),
		Limit:  deref(p.Limit),
	}
	if p.ServiceId != nil {
		f.ServiceID = uuid(*p.ServiceId)
	}
	if p.Status != nil {
		for _, st := range *p.Status {
			f.Statuses = append(f.Statuses, booking.Status(st))
		}
	}
	page, err := booking.ListAppointments(ctx, db.New(s.Pool), f, s.Now())
	if err != nil {
		return nil, err
	}
	out := gen.ListAppointments200JSONResponse{Timezone: page.Timezone, Items: make([]gen.AppointmentSummary, len(page.Items))}
	for i, r := range page.Items {
		out.Items[i] = gen.AppointmentSummary{
			Id:              openapi_types.UUID(r.ID.Bytes),
			Reference:       r.Reference,
			Status:          gen.AppointmentStatus(r.Status),
			Service:         serviceRef(r.ServiceID, r.ServiceSlug, r.ServiceName),
			StartsAt:        r.StartsAt.UTC(),
			EndsAt:          r.EndsAt.UTC(),
			DurationMinutes: int(r.DurationMinutes),
			Timezone:        r.Timezone,
			Format:          gen.Format(r.Format),
			Source:          gen.AppointmentSummarySource(r.Source),
			VisitorName:     r.VisitorName,
			HoldExpiresAt:   optionalTime(r.HoldExpiresAt.Time, r.HoldExpiresAt.Valid),
			CreatedAt:       r.CreatedAt.UTC(),
			UpdatedAt:       r.UpdatedAt.UTC(),
		}
	}
	out.NextCursor = nonEmpty(page.NextCursor)
	return out, nil
}

// GetAppointment shows one appointment with its history and messages.
func (s *server) GetAppointment(ctx context.Context, req gen.GetAppointmentRequestObject) (gen.GetAppointmentResponseObject, error) {
	d, err := s.appointmentDetail(ctx, uuid(req.AppointmentId))
	if err != nil {
		return nil, err
	}
	return gen.GetAppointment200JSONResponse(d), nil
}

// CreateManualAppointment stores an appointment Daw Mi made by hand and
// answers with its detail; a replay answers with the same appointment.
func (s *server) CreateManualAppointment(ctx context.Context, req gen.CreateManualAppointmentRequestObject) (gen.CreateManualAppointmentResponseObject, error) {
	b := req.Body
	raw, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	m := booking.Manual{
		IdempotencyKey: req.Params.IdempotencyKey.String(),
		Body:           raw,
		ServiceID:      uuid(b.ServiceId),
		StartsAt:       b.StartsAt,
		Format:         string(b.Format),
		Locale:         string(b.Locale),
		VisitorName:    b.Visitor.Name,
		VisitorEmail:   string(b.Visitor.Email),
		VisitorPhone:   deref(b.Visitor.Phone),
		VisitorNote:    deref(b.Visitor.Note),
		AdminNote:      deref(b.AdminNote),
		Status:         booking.Confirmed,
		Notify:         b.NotifyVisitor == nil || *b.NotifyVisitor,
		Actor:          actor(ctx),
	}
	if b.Status != nil {
		m.Status = booking.Status(*b.Status)
	}
	res, err := booking.CreateManual(ctx, s.Pool, s.SigningSecret, m, s.Now())
	if err != nil {
		return nil, err
	}
	s.afterChange(ctx, "appointment_created", res.AppointmentID, booking.Changed{Tasks: res.Tasks})
	d, err := s.appointmentDetail(ctx, res.AppointmentID)
	if err != nil {
		return nil, err
	}
	if res.Replayed {
		return replayedAppointment{gen.CreateManualAppointment201JSONResponse(d)}, nil
	}
	return gen.CreateManualAppointment201JSONResponse(d), nil
}

type replayedAppointment struct {
	gen.CreateManualAppointment201JSONResponse
}

func (r replayedAppointment) VisitCreateManualAppointmentResponse(w http.ResponseWriter) error {
	w.Header().Set("Idempotent-Replayed", "true")
	return r.CreateManualAppointment201JSONResponse.VisitCreateManualAppointmentResponse(w)
}

// ConfirmAppointment confirms a pending request. meetingLink belongs to the
// manual meeting-link issue and is ignored until then.
func (s *server) ConfirmAppointment(ctx context.Context, req gen.ConfirmAppointmentRequestObject) (gen.ConfirmAppointmentResponseObject, error) {
	d, err := s.applyChange(ctx, "appointment_confirmed",
		func(ctx context.Context, pool *pgxpool.Pool, c booking.Change, now time.Time) (booking.Changed, error) {
			return booking.Confirm(ctx, pool, s.SigningSecret, c, now)
		}, s.changeOf(ctx, req.AppointmentId, req.Body.Version))
	if err != nil {
		return nil, err
	}
	return gen.ConfirmAppointment200JSONResponse(d), nil
}

// DeclineAppointment declines a pending request.
func (s *server) DeclineAppointment(ctx context.Context, req gen.DeclineAppointmentRequestObject) (gen.DeclineAppointmentResponseObject, error) {
	c := s.changeOf(ctx, req.AppointmentId, req.Body.Version)
	c.ToVisitor = deref(req.Body.MessageToVisitor)
	d, err := s.applyChange(ctx, "appointment_declined", booking.Decline, c)
	if err != nil {
		return nil, err
	}
	return gen.DeclineAppointment200JSONResponse(d), nil
}

// RescheduleAppointment moves an appointment to a new free slot.
func (s *server) RescheduleAppointment(ctx context.Context, req gen.RescheduleAppointmentRequestObject) (gen.RescheduleAppointmentResponseObject, error) {
	c := s.changeOf(ctx, req.AppointmentId, req.Body.Version)
	c.Notify = req.Body.NotifyVisitor == nil || *req.Body.NotifyVisitor
	start := req.Body.StartsAt
	d, err := s.applyChange(ctx, "appointment_rescheduled",
		func(ctx context.Context, pool *pgxpool.Pool, c booking.Change, now time.Time) (booking.Changed, error) {
			return booking.Reschedule(ctx, pool, c, start, now)
		}, c)
	if err != nil {
		return nil, err
	}
	return gen.RescheduleAppointment200JSONResponse(d), nil
}

// CancelAppointment cancels a confirmed appointment as Daw Mi.
func (s *server) CancelAppointment(ctx context.Context, req gen.CancelAppointmentRequestObject) (gen.CancelAppointmentResponseObject, error) {
	c := s.changeOf(ctx, req.AppointmentId, req.Body.Version)
	c.ToVisitor = deref(req.Body.MessageToVisitor)
	c.Notify = req.Body.NotifyVisitor == nil || *req.Body.NotifyVisitor
	d, err := s.applyChange(ctx, "appointment_cancelled", booking.Cancel, c)
	if err != nil {
		return nil, err
	}
	return gen.CancelAppointment200JSONResponse(d), nil
}

// CompleteAppointment records that an appointment took place.
func (s *server) CompleteAppointment(ctx context.Context, req gen.CompleteAppointmentRequestObject) (gen.CompleteAppointmentResponseObject, error) {
	d, err := s.applyChange(ctx, "appointment_completed", booking.Complete, s.changeOf(ctx, req.AppointmentId, req.Body.Version))
	if err != nil {
		return nil, err
	}
	return gen.CompleteAppointment200JSONResponse(d), nil
}

// MarkAppointmentNoShow records that the visitor did not come.
func (s *server) MarkAppointmentNoShow(ctx context.Context, req gen.MarkAppointmentNoShowRequestObject) (gen.MarkAppointmentNoShowResponseObject, error) {
	d, err := s.applyChange(ctx, "appointment_no_show", booking.MarkNoShow, s.changeOf(ctx, req.AppointmentId, req.Body.Version))
	if err != nil {
		return nil, err
	}
	return gen.MarkAppointmentNoShow200JSONResponse(d), nil
}

// SetAppointmentNote replaces the private scheduling note.
func (s *server) SetAppointmentNote(ctx context.Context, req gen.SetAppointmentNoteRequestObject) (gen.SetAppointmentNoteResponseObject, error) {
	c := s.changeOf(ctx, req.AppointmentId, req.Body.Version)
	if err := booking.SetNote(ctx, s.Pool, c, req.Body.Note, s.Now()); err != nil {
		return nil, err
	}
	s.afterChange(ctx, "appointment_note_set", c.ID, booking.Changed{})
	d, err := s.appointmentDetail(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	return gen.SetAppointmentNote200JSONResponse(d), nil
}

// applyChange runs one admin action, then, after it has committed, updates
// the queue and reads the fresh detail, whose communications show what was
// queued.
func (s *server) applyChange(ctx context.Context, msg string,
	action func(context.Context, *pgxpool.Pool, booking.Change, time.Time) (booking.Changed, error), c booking.Change) (gen.AppointmentDetail, error) {
	changed, err := action(ctx, s.Pool, c, s.Now())
	if err != nil {
		return gen.AppointmentDetail{}, err
	}
	s.afterChange(ctx, msg, c.ID, changed)
	return s.appointmentDetail(ctx, c.ID)
}

// afterChange hands the queue what a committed change asks of it and logs
// the change by id only.
func (s *server) afterChange(ctx context.Context, msg string, id pgtype.UUID, changed booking.Changed) {
	if s.Queue != nil {
		s.Queue.Remove(changed.Remove...)
		s.Queue.Replace(ctx, changed.Replace...)
		s.Queue.Enqueue(ctx, changed.Tasks...)
	}
	s.Log.InfoContext(ctx, msg, "request_id", RequestID(ctx), "appointment_id", id.String())
}

func (s *server) changeOf(ctx context.Context, id openapi_types.UUID, version int) booking.Change {
	return booking.Change{ID: uuid(id), Version: int32(version), Actor: actor(ctx), Notify: true}
}

// actor is the signed-in administrator; the role check has already run.
func actor(ctx context.Context) pgtype.UUID {
	session, _ := auth.FromContext(ctx)
	return session.User.ID
}
