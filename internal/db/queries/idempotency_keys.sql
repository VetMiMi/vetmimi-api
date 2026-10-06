-- InsertIdempotencyKey waits for a transaction holding the same key, then
-- inserts nothing if that transaction committed.
-- name: InsertIdempotencyKey :execrows
INSERT INTO idempotency_keys (scope, key, request_hash, created_at)
VALUES (@scope, @key, @request_hash, @created_at)
ON CONFLICT (scope, key) DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE scope = @scope AND key = @key FOR UPDATE;

-- name: FinishIdempotencyKey :exec
UPDATE idempotency_keys
SET resource_id = @resource_id, response_status = @response_status, response_body = @response_body
WHERE scope = @scope AND key = @key;

-- DeleteExpiredIdempotencyKeys deletes up to @max_rows keys created before
-- @before, so one run never holds a long lock.
-- name: DeleteExpiredIdempotencyKeys :execrows
DELETE FROM idempotency_keys
WHERE ctid IN (
    SELECT k.ctid FROM idempotency_keys k WHERE k.created_at < @before LIMIT @max_rows
);
