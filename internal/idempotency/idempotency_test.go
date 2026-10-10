package idempotency_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/idempotency"
	"github.com/VetMiMi/vetmimi-api/internal/pgtest"
)

func TestMain(m *testing.M) { os.Exit(pgtest.Run(m)) }

var now = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)

// begin runs Begin in its own transaction and, for a new key, Finish with
// body as the response, committing unless fail is set.
func begin(t *testing.T, scope, key, body string, fail bool) (*idempotency.Stored, error) {
	t.Helper()
	var stored *idempotency.Stored
	errFail := errors.New("create failed")
	err := pgx.BeginFunc(context.Background(), pgtest.Pool(t), func(tx pgx.Tx) error {
		q := db.New(tx)
		var err error
		stored, err = idempotency.Begin(context.Background(), q, scope, key, []byte(body), now)
		if err != nil || stored != nil {
			return err
		}
		if fail {
			return errFail
		}
		return idempotency.Finish(context.Background(), q, scope, key, pgtype.UUID{}, 201, map[string]string{"body": body})
	})
	if errors.Is(err, errFail) {
		return nil, nil
	}
	return stored, err
}

func TestIdempotency_SameBodyReplays(t *testing.T) {
	stored, err := begin(t, idempotency.PublicAppointment, "same-body", `{"a":1}`, false)
	require.NoError(t, err)
	require.Nil(t, stored, "a new key creates")

	stored, err = begin(t, idempotency.PublicAppointment, "same-body", `{"a":1}`, false)
	require.NoError(t, err)
	require.NotNil(t, stored)
	require.Equal(t, 201, stored.Status)
	require.JSONEq(t, `{"body":"{\"a\":1}"}`, string(stored.Body))

	stored, err = begin(t, "admin_appointment", "same-body", `{"a":1}`, false)
	require.NoError(t, err)
	require.Nil(t, stored, "another scope does not collide")
}

func TestIdempotency_DifferentBodyRefused(t *testing.T) {
	_, err := begin(t, idempotency.PublicAppointment, "different-body", `{"a":1}`, false)
	require.NoError(t, err)
	_, err = begin(t, idempotency.PublicAppointment, "different-body", `{"a":2}`, false)
	var e *apperr.Error
	require.ErrorAs(t, err, &e)
	require.Equal(t, apperr.IdempotencyKeyReused, e.Code)
}

func TestIdempotency_RollbackLeavesNoKey(t *testing.T) {
	_, err := begin(t, idempotency.PublicAppointment, "rolled-back", `{"a":1}`, true)
	require.NoError(t, err)
	stored, err := begin(t, idempotency.PublicAppointment, "rolled-back", `{"a":2}`, false)
	require.NoError(t, err)
	require.Nil(t, stored, "the retry is evaluated afresh, whatever its body")
}

func TestCleanup_DeletesExpiredKeys(t *testing.T) {
	ctx := context.Background()
	q := db.New(pgtest.Pool(t))
	for key, age := range map[string]time.Duration{"old": 25 * time.Hour, "young": 23 * time.Hour} {
		_, err := q.InsertIdempotencyKey(ctx, db.InsertIdempotencyKeyParams{
			Scope: idempotency.PublicAppointment, Key: "cleanup-" + key, RequestHash: []byte("h"), CreatedAt: now.Add(-age),
		})
		require.NoError(t, err)
	}
	n, err := idempotency.DeleteExpired(ctx, q, now)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, int64(1))

	_, err = q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{Scope: idempotency.PublicAppointment, Key: "cleanup-old"})
	require.ErrorIs(t, err, pgx.ErrNoRows)
	_, err = q.GetIdempotencyKey(ctx, db.GetIdempotencyKeyParams{Scope: idempotency.PublicAppointment, Key: "cleanup-young"})
	require.NoError(t, err)
}
