-- name: ListAvailabilityBlocks :many
SELECT * FROM availability_blocks
WHERE period && @within::tstzrange
ORDER BY lower(period);

-- name: CreateAvailabilityBlock :one
INSERT INTO availability_blocks (period, all_day, reason, created_by)
VALUES (@period, @all_day, sqlc.narg(reason), @created_by)
RETURNING *;

-- name: UpdateAvailabilityBlock :one
UPDATE availability_blocks
SET period = @period, all_day = @all_day, reason = sqlc.narg(reason), updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteAvailabilityBlock :execrows
DELETE FROM availability_blocks WHERE id = @id;
