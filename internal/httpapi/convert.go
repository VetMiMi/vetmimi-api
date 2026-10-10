package httpapi

import (
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/VetMiMi/vetmimi-api/internal/httpapi/gen"
)

// Conversions between the generated API types and the database types.

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func uuid(id openapi_types.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func optionalUUID(id *openapi_types.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return uuid(*id)
}

func uuids(ids *[]openapi_types.UUID) []pgtype.UUID {
	out := []pgtype.UUID{}
	for _, id := range deref(ids) {
		out = append(out, uuid(id))
	}
	return out
}

func uuidView(id pgtype.UUID) *openapi_types.UUID {
	if !id.Valid {
		return nil
	}
	return new(openapi_types.UUID(id.Bytes))
}

func uuidsView(ids []pgtype.UUID) *[]openapi_types.UUID {
	out := make([]openapi_types.UUID, len(ids))
	for i, id := range ids {
		out[i] = openapi_types.UUID(id.Bytes)
	}
	return &out
}

func optionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func optionalString(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

func optionalInt4(n *int) pgtype.Int4 {
	if n == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*n), Valid: true}
}

func optionalTime(t time.Time, valid bool) *time.Time {
	if !valid {
		return nil
	}
	return new(t.UTC())
}

func dateOf(d *openapi_types.Date) *time.Time {
	if d == nil {
		return nil
	}
	return &d.Time
}

// localizedJSON stores text per locale; nil stays NULL.
func localizedJSON(t *gen.LocalizedText) json.RawMessage {
	if t == nil {
		return nil
	}
	b, _ := json.Marshal(t) // two optional strings always marshal
	return b
}

func localizedView(raw json.RawMessage) *gen.LocalizedText {
	if raw == nil {
		return nil
	}
	var t gen.LocalizedText
	if json.Unmarshal(raw, &t) != nil {
		return nil
	}
	return &t
}

func serviceRef(id pgtype.UUID, slug string, name json.RawMessage) gen.ServiceRef {
	return gen.ServiceRef{Id: openapi_types.UUID(id.Bytes), Slug: slug, Name: deref(localizedView(name))}
}
