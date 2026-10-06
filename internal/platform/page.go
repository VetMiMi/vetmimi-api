package platform

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// ErrCursor refuses a cursor no list returned.
var ErrCursor = apperr.Invalid("The cursor is not one this list returned.",
	apperr.FieldError{Field: "cursor", Message: "is malformed"})

// EncodeCursor is a keyset list's position after its last row, by the row's
// sort time and id, opaque to the client.
func EncodeCursor(at time.Time, id pgtype.UUID) string {
	return base64.RawURLEncoding.EncodeToString(fmt.Appendf(nil, "%d|%s", at.UnixMicro(), id.String()))
}

// DecodeCursor reads what EncodeCursor wrote, or fails with ErrCursor.
func DecodeCursor(c string) (sql.NullTime, pgtype.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return sql.NullTime{}, pgtype.UUID{}, ErrCursor
	}
	var micros int64
	var id pgtype.UUID
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok || id.Scan(rest) != nil {
		return sql.NullTime{}, pgtype.UUID{}, ErrCursor
	}
	if _, err := fmt.Sscan(at, &micros); err != nil {
		return sql.NullTime{}, pgtype.UUID{}, ErrCursor
	}
	return sql.NullTime{Time: time.UnixMicro(micros).UTC(), Valid: true}, id, nil
}

// LikePattern matches s anywhere, with ILIKE's wildcards in s taken
// literally; empty s matches nothing to filter by.
func LikePattern(s string) pgtype.Text {
	s = strings.TrimSpace(s)
	if s == "" {
		return pgtype.Text{}
	}
	s = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
	return pgtype.Text{String: "%" + s + "%", Valid: true}
}
