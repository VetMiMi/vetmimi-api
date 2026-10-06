package booking

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Event is one entry in an appointment's history. From and To are empty
// unless the status changed; Previous and New are set for reschedules.
type Event struct {
	AppointmentID pgtype.UUID
	Kind          string
	From, To      Status
	Previous, New *Period
	Actor         string
	ActorUserID   pgtype.UUID
	Detail        EventDetail
}

// EventDetail is the non-personal facts an event may record. It is a struct,
// not a map, so a name, email or note has nowhere to go.
type EventDetail struct {
	LateCancellation *bool `json:"late_cancellation,omitempty"`
}

// AppendEvent records e in the caller's transaction.
func AppendEvent(ctx context.Context, q db.Querier, e Event) error {
	detail, err := json.Marshal(e.Detail)
	if err != nil {
		return err
	}
	return q.InsertAppointmentEvent(ctx, db.InsertAppointmentEventParams{
		AppointmentID: e.AppointmentID,
		Kind:          e.Kind,
		FromStatus:    pgtype.Text{String: string(e.From), Valid: e.From != ""},
		ToStatus:      pgtype.Text{String: string(e.To), Valid: e.To != ""},
		PreviousRange: optionalRange(e.Previous),
		NewRange:      optionalRange(e.New),
		Actor:         e.Actor,
		ActorUserID:   e.ActorUserID,
		Detail:        detail,
	})
}

func optionalRange(p *Period) pgtype.Range[pgtype.Timestamptz] {
	if p == nil {
		return pgtype.Range[pgtype.Timestamptz]{}
	}
	return p.tstzrange()
}
