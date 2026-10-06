package booking

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/VetMiMi/vetmimi-api/internal/db"
	"github.com/VetMiMi/vetmimi-api/internal/platform/apperr"
)

// Block is time the practitioner is unavailable. AllDay only tells the admin
// screen how it was entered; Period is the truth. Reason is private.
type Block struct {
	Period Period
	AllDay bool
	Reason pgtype.Text
}

// SavedBlock is a block as saved and the pending or confirmed appointments it
// overlaps. Those appointments are left exactly as they were: the admin
// decides what happens to each (Booking & Admin UX, section 19).
type SavedBlock struct {
	Block     db.AvailabilityBlock
	Conflicts []db.ListOverlappingAppointmentsRow
}

var errBlockNotFound = apperr.New(apperr.NotFound, "No block has this id.")

// ListBlocks lists the blocks that overlap within.
func ListBlocks(ctx context.Context, q db.Querier, within Period) ([]db.AvailabilityBlock, error) {
	return q.ListAvailabilityBlocks(ctx, within.tstzrange())
}

// CreateBlock saves b, made by an administrator, even when it overlaps
// appointments.
func CreateBlock(ctx context.Context, pool *pgxpool.Pool, b Block, by pgtype.UUID) (SavedBlock, error) {
	if err := checkBlock(b); err != nil {
		return SavedBlock{}, err
	}
	var saved SavedBlock
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		saved.Block, err = q.CreateAvailabilityBlock(ctx, db.CreateAvailabilityBlockParams{
			Period: b.Period.tstzrange(), AllDay: b.AllDay, Reason: b.Reason, CreatedBy: by})
		if err != nil {
			return err
		}
		saved.Conflicts, err = q.ListOverlappingAppointments(ctx, b.Period.tstzrange())
		return err
	})
	return saved, err
}

// UpdateBlock replaces a block with b, as CreateBlock saves one.
func UpdateBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID, b Block, now time.Time) (SavedBlock, error) {
	if err := checkBlock(b); err != nil {
		return SavedBlock{}, err
	}
	var saved SavedBlock
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		saved.Block, err = q.UpdateAvailabilityBlock(ctx, db.UpdateAvailabilityBlockParams{
			ID: id, Period: b.Period.tstzrange(), AllDay: b.AllDay, Reason: b.Reason, Now: now})
		if err != nil {
			return err
		}
		saved.Conflicts, err = q.ListOverlappingAppointments(ctx, b.Period.tstzrange())
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedBlock{}, errBlockNotFound
	}
	return saved, err
}

// DeleteBlock deletes a block.
func DeleteBlock(ctx context.Context, pool *pgxpool.Pool, id pgtype.UUID) error {
	var n int64
	err := inSchedule(ctx, pool, func(q *db.Queries) (err error) {
		n, err = q.DeleteAvailabilityBlock(ctx, id)
		return err
	})
	if err == nil && n == 0 {
		return errBlockNotFound
	}
	return err
}

func checkBlock(b Block) error {
	if !b.Period.End.After(b.Period.Start) {
		e := unprocessable("/endsAt", "must be later than startsAt")
		return &e
	}
	return nil
}
