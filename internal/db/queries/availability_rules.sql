-- name: ListAvailabilityRules :many
SELECT * FROM availability_rules ORDER BY weekday, start_time;

-- name: CreateAvailabilityRule :one
INSERT INTO availability_rules (weekday, start_time, end_time)
VALUES (@weekday, @start_time, @end_time)
RETURNING *;

-- name: UpdateAvailabilityRule :one
UPDATE availability_rules
SET weekday = @weekday, start_time = @start_time, end_time = @end_time, updated_at = @now
WHERE id = @id
RETURNING *;

-- name: DeleteAvailabilityRule :execrows
DELETE FROM availability_rules WHERE id = @id;
