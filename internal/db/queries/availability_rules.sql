-- name: ListAvailabilityRules :many
SELECT * FROM availability_rules ORDER BY weekday, start_time;

-- name: CreateAvailabilityRule :one
INSERT INTO availability_rules (weekday, start_time, end_time)
VALUES (@weekday, @start_time, @end_time)
RETURNING *;

-- name: DeleteAllAvailabilityRules :exec
DELETE FROM availability_rules;
