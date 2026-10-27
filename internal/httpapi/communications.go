package httpapi

import (
	"context"

	"github.com/VetMiMi/vetmimi-api/internal/comms"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

func (s *server) ResendCommunication(ctx context.Context, req gen.ResendCommunicationRequestObject) (gen.ResendCommunicationResponseObject, error) {
	row, task, err := comms.Resend(ctx, s.Pool, uuid(req.AppointmentId), uuid(req.CommunicationId), actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.enqueue(ctx, task)
	s.Log.InfoContext(ctx, "communication resent", "request_id", RequestID(ctx),
		"communication_id", row.ID.String(), "resend_of", req.CommunicationId.String())
	return gen.ResendCommunication201JSONResponse(communicationView(row)), nil
}

func (s *server) MarkAppointmentCommunicated(ctx context.Context, req gen.MarkAppointmentCommunicatedRequestObject) (gen.MarkAppointmentCommunicatedResponseObject, error) {
	row, err := comms.MarkCommunicated(ctx, db.New(s.Pool), uuid(req.AppointmentId), comms.Kind(req.Body.Kind),
		req.Body.Note, actor(ctx), s.Now())
	if err != nil {
		return nil, err
	}
	s.Log.InfoContext(ctx, "communication recorded", "request_id", RequestID(ctx),
		"appointment_id", req.AppointmentId.String(), "communication_id", row.ID.String())
	return gen.MarkAppointmentCommunicated201JSONResponse(communicationView(row)), nil
}
