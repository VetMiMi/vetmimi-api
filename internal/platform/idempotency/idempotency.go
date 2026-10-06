// Package idempotency stores the response of a create under the client's
// Idempotency-Key, so a retried submission returns the first result instead
// of creating twice (ADR-004). Both calls run in the creating transaction:
// a create that rolls back leaves no key, and its retry is evaluated afresh.
package idempotency

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// The scopes of the keys: a visitor's appointment request, an appointment
// Daw Mi makes by hand, and a contact enquiry.
const (
	PublicAppointment = "public_appointment"
	AdminAppointment  = "admin_appointment"
	ContactEnquiry    = "contact_enquiry"
)

// Lifetime is how long a key is kept.
const Lifetime = 24 * time.Hour

// deleteBatch bounds one delete, so cleanup never holds a long lock.
const deleteBatch = 500

// Stored is the response a key already holds.
type Stored struct {
	ResourceID pgtype.UUID
	Status     int
	Body       json.RawMessage
}

var errReused = apperr.New(apperr.IdempotencyKeyReused,
	"The Idempotency-Key was already used with a different body.")

// Begin claims key in scope for body, the request body the handler hashes.
// It returns nil when the key is new and the caller should create. When
// another transaction holds the key, Begin waits for it; if it committed,
// Begin returns its stored response for the same body, or
// idempotency_key_reused for a different one.
func Begin(ctx context.Context, q db.Querier, scope, key string, body []byte, now time.Time) (*Stored, error) {
	hash := sha256.Sum256(body)
	n, err := q.InsertIdempotencyKey(ctx, db.InsertIdempotencyKeyParams{
		Scope: scope, Key: key, RequestHash: hash[:], CreatedAt: now,
	})
	if err != nil || n == 1 {
		return nil, err
	}
	row, err := q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{Scope: scope, Key: key})
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(row.RequestHash, hash[:]) {
		return nil, errReused
	}
	return &Stored{ResourceID: row.ResourceID, Status: int(row.ResponseStatus.Int16), Body: row.ResponseBody}, nil
}

// Finish stores the response to key, in the transaction Begin ran in.
func Finish(ctx context.Context, q db.Querier, scope, key string, resourceID pgtype.UUID, status int, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return q.FinishIdempotencyKey(ctx, db.FinishIdempotencyKeyParams{
		Scope: scope, Key: key, ResourceID: resourceID,
		ResponseStatus: pgtype.Int2{Int16: int16(status), Valid: true}, ResponseBody: raw,
	})
}

// DeleteExpired deletes keys older than Lifetime, a batch at a time, and
// returns how many went.
func DeleteExpired(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	var total int64
	for {
		n, err := q.DeleteExpiredIdempotencyKeys(ctx, db.DeleteExpiredIdempotencyKeysParams{
			Before: now.Add(-Lifetime), MaxRows: deleteBatch,
		})
		total += n
		if err != nil || n < deleteBatch {
			return total, err
		}
	}
}
