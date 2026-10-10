package httpapi

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/booking"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// appointmentDetail reads an appointment as the admin's detail screen shows
// it, with every visitor field, its history and its messages.
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

func serviceRef(id pgtype.UUID, slug string, name json.RawMessage) gen.ServiceRef {
	return gen.ServiceRef{Id: openapi_types.UUID(id.Bytes), Slug: slug, Name: deref(localizedView(name))}
}

func optionalString(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func optionalTime(t time.Time, valid bool) *time.Time {
	if !valid {
		return nil
	}
	return new(t.UTC())
}
