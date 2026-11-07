// Package listing holds what the admin lists share: keyset cursors and the
// ILIKE search pattern.
package listing

import (
	"database/sql"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
)

var errCursor = apperr.Invalid("The cursor is not one this list returned.", apperr.FieldError{Field: "cursor", Message: "is malformed"})

// EncodeCursor is the position after a list's last row, by its sort time and
// id, opaque to the client.
func EncodeCursor(at time.Time, id pgtype.UUID) string {
	position := fmt.Appendf(nil, "%d|%s", at.UnixMicro(), id.String())
	return base64.RawURLEncoding.EncodeToString(position)
}

// DecodeCursor reads what EncodeCursor wrote, or fails with errCursor.
func DecodeCursor(cursor string) (sql.NullTime, pgtype.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	var micros int64
	var id pgtype.UUID
	at, rest, ok := strings.Cut(string(raw), "|")
	if !ok || id.Scan(rest) != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	_, err = fmt.Sscan(at, &micros)
	if err != nil {
		return sql.NullTime{}, pgtype.UUID{}, errCursor
	}
	return sql.NullTime{Time: time.UnixMicro(micros).UTC(), Valid: true}, id, nil
}

// LikePattern matches search anywhere, with ILIKE's wildcards in search taken
// literally; an empty search matches nothing to filter by.
func LikePattern(search string) pgtype.Text {
	search = strings.TrimSpace(search)
	if search == "" {
		return pgtype.Text{}
	}
	search = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search)
	return pgtype.Text{String: "%" + search + "%", Valid: true}
}
