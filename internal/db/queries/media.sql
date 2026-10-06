-- name: CreateMedia :one
INSERT INTO media (width, height, widths, byte_size, alt, credit, uploaded_by, created_at, updated_at)
VALUES (@width, @height, @widths, @byte_size, sqlc.narg(alt), sqlc.narg(credit), @uploaded_by, @now, @now)
RETURNING *;

-- name: GetMedia :one
SELECT * FROM media WHERE id = @id;

-- ListMedia is newest first; the page continues after (@after_at, @after_id).
-- name: ListMedia :many
SELECT * FROM media
WHERE (sqlc.narg(search)::text IS NULL
       OR alt->>'en' ILIKE sqlc.narg(search) OR alt->>'my' ILIKE sqlc.narg(search)
       OR credit ILIKE sqlc.narg(search))
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (created_at, id) < (sqlc.narg(after_at), @after_id::uuid))
ORDER BY created_at DESC, id DESC
LIMIT @max_rows;

-- UpdateMedia changes the description as of @version; no row back means the
-- item is gone or changed since it was read.
-- name: UpdateMedia :one
UPDATE media
SET alt = sqlc.narg(alt), credit = sqlc.narg(credit), version = version + 1, updated_at = @now
WHERE id = @id AND version = @version
RETURNING *;

-- DeleteUnusedMedia deletes an item no post version shows, whatever the
-- post's status; no row back means it is gone or in use.
-- name: DeleteUnusedMedia :one
DELETE FROM media
WHERE id = @id
  AND NOT EXISTS (SELECT 1 FROM post_versions v
                  WHERE v.cover_image_id = @id OR @id = ANY (v.image_ids))
RETURNING *;

-- name: ExistingMedia :many
SELECT id FROM media WHERE id = ANY (@ids::uuid[]);
