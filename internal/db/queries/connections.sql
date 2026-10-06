-- name: GetConnection :one
SELECT * FROM connections WHERE platform = @platform;

-- SaveConnection replaces a platform's connection whole.
-- name: SaveConnection :one
INSERT INTO connections (platform, status, token, account_id, account_name, instagram_id,
                         instagram_username, expires_at, last_error, connected_by, connected_at, updated_at)
VALUES (@platform, @status, @token, sqlc.narg(account_id), sqlc.narg(account_name), sqlc.narg(instagram_id),
        sqlc.narg(instagram_username), sqlc.narg(expires_at), NULL, @connected_by, @now, @now)
ON CONFLICT (platform) DO UPDATE
SET status = excluded.status, token = excluded.token, account_id = excluded.account_id,
    account_name = excluded.account_name, instagram_id = excluded.instagram_id,
    instagram_username = excluded.instagram_username, expires_at = excluded.expires_at,
    last_error = NULL, connected_by = excluded.connected_by, connected_at = excluded.connected_at,
    updated_at = excluded.updated_at
RETURNING *;

-- name: SetConnectionError :exec
UPDATE connections SET last_error = @last_error, updated_at = @now WHERE platform = @platform;

-- name: DeleteConnection :exec
DELETE FROM connections WHERE platform = @platform;
