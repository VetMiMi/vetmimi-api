-- name: ListAvailabilityOverrides :many
SELECT * FROM availability_overrides
WHERE on_date BETWEEN @from_date AND @to_date
ORDER BY on_date, lower(period);

-- name: CreateAvailabilityOverride :one
INSERT INTO availability_overrides (on_date, kind, period, note, created_by)
VALUES (@on_date, @kind, @period, sqlc.narg(note), @created_by)
RETURNING *;

-- name: UpdateAvailabilityOverride :one
UPDATE availability_overrides
SET on_date = @on_date, kind = @kind, period = @period, note = sqlc.narg(note), updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteAvailabilityOverride :execrows
DELETE FROM availability_overrides WHERE id = @id;
