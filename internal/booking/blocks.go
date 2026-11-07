package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/apperr"
	"github.com/VetMiMi/vetmimi-api/internal/db"
)

// Block is time the practitioner is away; AllDay only records how it was entered.
type Block struct {
	Period Period
	AllDay bool
	Reason pgtype.Text
}

// SavedBlock lists the appointments a block overlaps; they are left for the admin to decide on.
type SavedBlock struct {
	Block     db.AvailabilityBlock
	Conflicts []db.ListOverlappingAppointmentsRow
}

var errBlockNotFound = apperr.New(apperr.NotFound, "No block has this id.")

func ListBlocks(ctx context.Context, q db.Querier, within Period) ([]db.AvailabilityBlock, error) {
	return q.ListAvailabilityBlocks(ctx, within.tstzrange())
}

func CreateBlock(ctx context.Context, pool *pgxpool.Pool, block Block, by pgtype.UUID) (SavedBlock, error) {
	err := checkBlock(block)
	if err != nil {
		return SavedBlock{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	params := db.CreateAvailabilityBlockParams{
		Period:    block.Period.tstzrange(),
		AllDay:    block.AllDay,
		Reason:    block.Reason,
		CreatedBy: by,
	}
	row, err := q.CreateAvailabilityBlock(ctx, params)
	if err != nil {
		return SavedBlock{}, err
	}
	conflicts, err := q.ListOverlappingAppointments(ctx, block.Period.tstzrange())
	if err != nil {
		return SavedBlock{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	return SavedBlock{Block: row, Conflicts: conflicts}, nil
}

func UpdateBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, block Block, now time.Time) (SavedBlock, error) {
	err := checkBlock(block)
	if err != nil {
		return SavedBlock{}, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	params := db.UpdateAvailabilityBlockParams{
		ID:     id,
		Period: block.Period.tstzrange(),
		AllDay: block.AllDay,
		Reason: block.Reason,
		Now:    now,
	}
	row, err := q.UpdateAvailabilityBlock(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedBlock{}, errBlockNotFound
	}
	if err != nil {
		return SavedBlock{}, err
	}
	conflicts, err := q.ListOverlappingAppointments(ctx, block.Period.tstzrange())
	if err != nil {
		return SavedBlock{}, err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return SavedBlock{}, err
	}
	return SavedBlock{Block: row, Conflicts: conflicts}, nil
}

func DeleteBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)

	err = q.LockSchedule(ctx)
	if err != nil {
		return err
	}
	deleted, err := q.DeleteAvailabilityBlock(ctx, id)
	if err != nil {
		return err
	}

	err = tx.Commit(ctx)
	if err != nil {
		return err
	}
	if deleted == 0 {
		return errBlockNotFound
	}
	return nil
}

func checkBlock(block Block) error {
	if !block.Period.End.After(block.Period.Start) {
		refused := unprocessable("/endsAt", "must be later than startsAt")
		return &refused
	}
	return nil
}
