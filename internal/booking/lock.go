package booking

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// inSchedule runs fn in a transaction holding the schedule lock, which
// availability writes and appointment creation share, so a block and a
// booking cannot interleave (docs/architecture.md, walkthrough 1, step 5).
func inSchedule(ctx context.Context, pool *pgxpool.Pool, fn func(q *db.Queries) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.LockSchedule(ctx); err != nil {
			return err
		}
		return fn(q)
	})
}
