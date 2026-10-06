-- name: ListSettings :many
SELECT key, value, updated_at FROM settings;

-- UpdateSettings writes every key of patch, a JSON object of key to value, in
-- one statement, so a patch lands whole or not at all. Keys with no row are
-- ignored; platform/settings only passes known keys.
-- name: UpdateSettings :exec
UPDATE settings s
SET value = p.value, updated_by = @updated_by, updated_at = @now
FROM jsonb_each(@patch::jsonb) AS p(key, value)
WHERE s.key = p.key;
