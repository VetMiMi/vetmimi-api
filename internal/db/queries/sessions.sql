-- name: CreateSession :one
INSERT INTO sessions (user_id, token_hash, last_seen_at, expires_at)
VALUES (@user_id, @token_hash, @now, @expires_at)
RETURNING id;

-- GetSession returns the session a token hash names together with its user,
-- so authenticating a request is one query.
-- name: GetSession :one
SELECT s.id, s.last_seen_at, s.expires_at,
       u.id AS user_id, u.email, u.display_name, u.roles, u.is_practitioner, u.disabled_at,
       (u.totp_secret_enc IS NOT NULL)::boolean AS two_step_enabled
FROM sessions s
JOIN users u ON u.id = s.user_id
WHERE s.token_hash = @token_hash;

-- name: TouchSession :exec
UPDATE sessions SET last_seen_at = @now WHERE id = @id;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = @id;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = @user_id;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= @now OR last_seen_at <= @idle_before;
