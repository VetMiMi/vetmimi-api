package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

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
	return gen.ListAppointments200JSONResponse{
		Timezone:   page.Timezone,
		Items:      appointmentSummaries(page.Items),
		NextCursor: nonEmpty(page.NextCursor),
	}, nil
}

func (s *server) GetBookingDashboard(ctx context.Context, _ gen.GetBookingDashboardRequestObject) (gen.GetBookingDashboardResponseObject, error) {
	d, err := booking.Dashboard(ctx, db.New(s.Pool), s.Now())
	if err != nil {
		return nil, err
	}
	out := gen.GetBookingDashboard200JSONResponse{
		Timezone:          d.Timezone,
		AttentionRequired: make([]gen.AttentionItem, len(d.Attention)),
		Pending:           appointmentSummaries(d.Pending),
		Today:             appointmentSummaries(d.Today),
		Upcoming:          appointmentSummaries(d.Upcoming),
	}
	out.Counts.Pending = int(d.Counts.Pending)
	out.Counts.Confirmed = int(d.Counts.Confirmed)
	out.Counts.Today = int(d.Counts.Today)
	out.Counts.ThisWeek = int(d.Counts.ThisWeek)
	for i, a := range d.Attention {
		out.AttentionRequired[i] = gen.AttentionItem{
			Kind:          gen.AttentionItemKind(a.Kind),
			AppointmentId: openapi_types.UUID(a.AppointmentID.Bytes),
			Reference:     a.Reference,
			StartsAt:      new(a.StartsAt.UTC()),
			Detail:        new(a.Detail),
		}
	}
	return out, nil
}

func (s *server) GetAppointment(ctx context.Context, req gen.GetAppointmentRequestObject) (gen.GetAppointmentResponseObject, error) {
	d, err := s.appointmentDetail(ctx, uuid(req.AppointmentId))
	if err != nil {
		return nil, err
	}
	return gen.GetAppointment200JSONResponse(d), nil
}

// CreateManualAppointment stores an appointment Daw Mi made by hand; a replay
// answers with the same appointment.
func (s *server) CreateManualAppointment(ctx context.Context, req gen.CreateManualAppointmentRequestObject) (gen.CreateManualAppointmentResponseObject, error) {
	m, err := manualAppointment(ctx, req)
	if err != nil {
		return nil, err
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

func manualAppointment(ctx context.Context, req gen.CreateManualAppointmentRequestObject) (booking.Manual, error) {
	b := req.Body
	raw, err := json.Marshal(b)
	if err != nil {
		return booking.Manual{}, err
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
	return m, nil
}

type replayedAppointment struct {
	gen.CreateManualAppointment201JSONResponse
}

func (r replayedAppointment) VisitCreateManualAppointmentResponse(w http.ResponseWriter) error {
	w.Header().Set("Idempotent-Replayed", "true")
	return r.CreateManualAppointment201JSONResponse.VisitCreateManualAppointmentResponse(w)
}

// ConfirmAppointment ignores meetingLink, which has no domain support yet.
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

func (s *server) DeclineAppointment(ctx context.Context, req gen.DeclineAppointmentRequestObject) (gen.DeclineAppointmentResponseObject, error) {
	c := s.changeOf(ctx, req.AppointmentId, req.Body.Version)
	c.ToVisitor = deref(req.Body.MessageToVisitor)
	d, err := s.applyChange(ctx, "appointment_declined", booking.Decline, c)
	if err != nil {
		return nil, err
	}
	return gen.DeclineAppointment200JSONResponse(d), nil
}

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

func (s *server) CompleteAppointment(ctx context.Context, req gen.CompleteAppointmentRequestObject) (gen.CompleteAppointmentResponseObject, error) {
	d, err := s.applyChange(ctx, "appointment_completed", booking.Complete, s.changeOf(ctx, req.AppointmentId, req.Body.Version))
	if err != nil {
		return nil, err
	}
	return gen.CompleteAppointment200JSONResponse(d), nil
}

func (s *server) MarkAppointmentNoShow(ctx context.Context, req gen.MarkAppointmentNoShowRequestObject) (gen.MarkAppointmentNoShowResponseObject, error) {
	d, err := s.applyChange(ctx, "appointment_no_show", booking.MarkNoShow, s.changeOf(ctx, req.AppointmentId, req.Body.Version))
	if err != nil {
		return nil, err
	}
	return gen.MarkAppointmentNoShow200JSONResponse(d), nil
}

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

// applyChange runs one admin action and, after it has committed, updates the
// queue and answers with the fresh detail.
func (s *server) applyChange(ctx context.Context, msg string,
	action func(context.Context, *pgxpool.Pool, booking.Change, time.Time) (booking.Changed, error), c booking.Change) (gen.AppointmentDetail, error) {
	changed, err := action(ctx, s.Pool, c, s.Now())
	if err != nil {
		return gen.AppointmentDetail{}, err
	}
	s.afterChange(ctx, msg, c.ID, changed)
	return s.appointmentDetail(ctx, c.ID)
}

// afterChange applies a committed change and logs it by id only.
func (s *server) afterChange(ctx context.Context, msg string, id pgtype.UUID, changed booking.Changed) {
	s.afterCommit(ctx, changed)
	s.Log.InfoContext(ctx, msg, "request_id", RequestID(ctx), "appointment_id", id.String())
}

// afterCommit hands the queue what a committed change asks of it and closes
// the sockets of a video room it ended.
func (s *server) afterCommit(ctx context.Context, changed booking.Changed) {
	if s.Queue != nil {
		s.Queue.Remove(changed.Remove...)
		s.Queue.Replace(ctx, changed.Replace...)
		s.Queue.Enqueue(ctx, changed.Tasks...)
	}
	s.Hub.EndRoom(changed.EndedRoom)
}

func (s *server) changeOf(ctx context.Context, id openapi_types.UUID, version int) booking.Change {
	return booking.Change{ID: uuid(id), Version: int32(version), Actor: actor(ctx), Notify: true}
}

// appointmentDetail is the admin's detail screen: every visitor field, the
// history and the messages.
func (s *server) appointmentDetail(ctx context.Context, id pgtype.UUID) (gen.AppointmentDetail, error) {
	d, err := booking.GetAppointment(ctx, db.New(s.Pool), id, s.Now())
	if err != nil {
		return gen.AppointmentDetail{}, err
	}
	a := d.Appointment
	out := gen.AppointmentDetail{
		Id:                    openapi_types.UUID(a.ID.Bytes),
		Reference:             a.Reference,
		Status:                gen.AppointmentStatus(a.Status),
		Service:               serviceRef(a.ServiceID, a.ServiceSlug, a.ServiceName),
		StartsAt:              a.StartsAt.UTC(),
		EndsAt:                a.EndsAt.UTC(),
		DurationMinutes:       int(a.DurationMinutes),
		Timezone:              a.Timezone,
		Format:                gen.Format(a.Format),
		Source:                gen.AppointmentDetailSource(a.Source),
		VisitorName:           a.VisitorName,
		VisitorEmail:          openapi_types.Email(a.VisitorEmail),
		VisitorPhone:          optionalString(a.VisitorPhone),
		VisitorNote:           optionalString(a.VisitorNote),
		AdminNote:             optionalString(a.AdminNote),
		Locale:                gen.Locale(a.Locale),
		MeetingLink:           optionalString(a.MeetingLink),
		LateCancellation:      a.LateCancellation,
		HoldExpiresAt:         optionalTime(a.HoldExpiresAt.Time, a.HoldExpiresAt.Valid),
		PrivacyAcknowledgedAt: optionalTime(a.PrivacyAckAt.Time, a.PrivacyAckAt.Valid),
		PolicyAcknowledgedAt:  optionalTime(a.PolicyAckAt.Time, a.PolicyAckAt.Valid),
		Version:               int(a.Version),
		CreatedAt:             a.CreatedAt.UTC(),
		UpdatedAt:             a.UpdatedAt.UTC(),
		AllowedActions:        make([]gen.AppointmentAction, len(d.AllowedActions)),
		Events:                make([]gen.AppointmentEvent, len(d.Events)),
		Communications:        make([]gen.Communication, len(d.Communications)),
	}
	if d.VideoRoom != nil {
		out.VideoRoom = videoRoomView(*d.VideoRoom)
	}
	for i, action := range d.AllowedActions {
		out.AllowedActions[i] = gen.AppointmentAction(action)
	}
	for i, e := range d.Events {
		out.Events[i] = eventView(e)
	}
	for i, c := range d.Communications {
		out.Communications[i] = communicationView(c)
	}
	return out, nil
}

func appointmentSummaries(rows []db.ListAppointmentsRow) []gen.AppointmentSummary {
	out := make([]gen.AppointmentSummary, len(rows))
	for i, r := range rows {
		out[i] = appointmentSummary(r)
	}
	return out
}

func appointmentSummary(r db.ListAppointmentsRow) gen.AppointmentSummary {
	return gen.AppointmentSummary{
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

func videoRoomView(r db.VideoRoom) *gen.VideoRoomSummary {
	return &gen.VideoRoomSummary{
		Id:        openapi_types.UUID(r.ID.Bytes),
		State:     gen.VideoRoomSummaryState(r.State),
		OpensAt:   r.OpensAt.UTC(),
		ClosesAt:  r.ClosesAt.UTC(),
		StartedAt: optionalTime(r.StartedAt.Time, r.StartedAt.Valid),
		EndedAt:   optionalTime(r.EndedAt.Time, r.EndedAt.Valid),
	}
}

func eventView(e db.ListAppointmentEventsRow) gen.AppointmentEvent {
	v := gen.AppointmentEvent{
		Id:        e.ID,
		Kind:      gen.AppointmentEventKind(e.Kind),
		Actor:     gen.AppointmentEventActor(e.Actor),
		ActorName: optionalString(e.ActorName),
		Detail:    map[string]any{},
		CreatedAt: e.CreatedAt.UTC(),
	}
	// The column is a JSON object by its CHECK constraint.
	_ = json.Unmarshal(e.Detail, &v.Detail)
	if e.FromStatus.Valid {
		v.FromStatus = new(gen.AppointmentStatus(e.FromStatus.String))
	}
	if e.ToStatus.Valid {
		v.ToStatus = new(gen.AppointmentStatus(e.ToStatus.String))
	}
	if e.PreviousRange.Valid {
		v.PreviousStartsAt = new(e.PreviousRange.Lower.Time.UTC())
		v.PreviousEndsAt = new(e.PreviousRange.Upper.Time.UTC())
	}
	if e.NewRange.Valid {
		v.NewStartsAt = new(e.NewRange.Lower.Time.UTC())
		v.NewEndsAt = new(e.NewRange.Upper.Time.UTC())
	}
	return v
}

// communicationView leaves out Daw Mi's message to the visitor, which the
// contract has no field for; it is in the email itself.
func communicationView(c db.Communication) gen.Communication {
	v := gen.Communication{
		Id:           openapi_types.UUID(c.ID.Bytes),
		Kind:         gen.CommunicationKind(c.Kind),
		Audience:     gen.CommunicationAudience(c.Audience),
		Channel:      gen.CommunicationChannel(c.Channel),
		Recipient:    optionalString(c.Recipient),
		Locale:       gen.Locale(c.Locale),
		Status:       gen.CommunicationStatus(c.Status),
		ScheduledFor: c.ScheduledFor.UTC(),
		SentAt:       optionalTime(c.SentAt.Time, c.SentAt.Valid),
		Error:        optionalString(c.Error),
		Attempts:     int(c.Attempts),
		Note:         optionalString(c.Note),
		CreatedAt:    c.CreatedAt.UTC(),
	}
	if c.AppointmentID.Valid {
		v.AppointmentId = new(openapi_types.UUID(c.AppointmentID.Bytes))
	}
	if c.ResendOf.Valid {
		v.ResendOf = new(openapi_types.UUID(c.ResendOf.Bytes))
	}
	return v
}
