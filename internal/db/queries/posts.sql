-- name: CreatePost :one
INSERT INTO posts (title, kind, status, consent_confirmed_at, consent_confirmed_by, consent_note,
                   author_id, created_at, updated_at)
VALUES (@title, @kind, @status, sqlc.narg(consent_confirmed_at), sqlc.narg(consent_confirmed_by),
        sqlc.narg(consent_note), @author_id, @now, @now)
RETURNING *;

-- name: GetPost :one
SELECT * FROM posts WHERE id = @id;

-- LockPost reads a post for a change, holding it until the transaction ends.
-- name: LockPost :one
SELECT * FROM posts WHERE id = @id FOR UPDATE;

-- SavePost writes every field a change may touch; the caller holds the lock.
-- name: SavePost :one
UPDATE posts
SET title = @title, kind = @kind, status = @status, scheduled_at = sqlc.narg(scheduled_at),
    consent_confirmed_at = sqlc.narg(consent_confirmed_at),
    consent_confirmed_by = sqlc.narg(consent_confirmed_by), consent_note = sqlc.narg(consent_note),
    review_note = sqlc.narg(review_note), approved_at = sqlc.narg(approved_at),
    approved_by = sqlc.narg(approved_by), published_at = sqlc.narg(published_at),
    version = version + 1, updated_at = @now
WHERE id = @id
RETURNING *;

-- DeletePost deletes only ideas and drafts; anything further is archived.
-- name: DeletePost :execrows
DELETE FROM posts WHERE id = @id AND status IN ('idea', 'draft');

-- ListPosts is newest first; the page continues after (@after_at, @after_id).
-- name: ListPosts :many
SELECT p.*,
       ARRAY(SELECT v.channel FROM post_versions v
             WHERE v.post_id = p.id AND v.enabled ORDER BY v.channel)::text[] AS channels
FROM posts p
WHERE (sqlc.narg(status)::text IS NULL OR p.status = sqlc.narg(status))
  AND (sqlc.narg(search)::text IS NULL OR p.title ILIKE sqlc.narg(search))
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (p.created_at, p.id) < (sqlc.narg(after_at), @after_id::uuid))
ORDER BY p.created_at DESC, p.id DESC
LIMIT @max_rows;

-- name: ListPostVersions :many
SELECT * FROM post_versions WHERE post_id = @post_id ORDER BY channel;

-- name: SavePostVersion :exec
INSERT INTO post_versions (post_id, channel, enabled, slug, title, excerpt, body, cover_image_id,
                           seo_title, seo_description, text, link_url, image_ids, updated_at)
VALUES (@post_id, @channel, @enabled, sqlc.narg(slug), sqlc.narg(title), sqlc.narg(excerpt),
        sqlc.narg(body), sqlc.narg(cover_image_id), sqlc.narg(seo_title), sqlc.narg(seo_description),
        sqlc.narg(text), sqlc.narg(link_url), @image_ids, @now)
ON CONFLICT (post_id, channel) DO UPDATE
SET enabled = excluded.enabled, slug = excluded.slug, title = excluded.title,
    excerpt = excluded.excerpt, body = excluded.body, cover_image_id = excluded.cover_image_id,
    seo_title = excluded.seo_title, seo_description = excluded.seo_description,
    text = excluded.text, link_url = excluded.link_url, image_ids = excluded.image_ids,
    updated_at = excluded.updated_at;

-- name: ListPostPublications :many
SELECT * FROM post_publications WHERE post_id = @post_id ORDER BY channel;

-- OpenPostPublication opens a channel's publication. Publishing a post again
-- (after its website version was edited) republishes the website, keeping
-- its first publication time, and leaves every social channel as it is.
-- name: OpenPostPublication :exec
INSERT INTO post_publications (post_id, channel, status, published_at, updated_at)
VALUES (@post_id, @channel, @status, sqlc.narg(published_at), @now)
ON CONFLICT (post_id, channel) DO UPDATE
SET status = excluded.status, error = NULL,
    published_at = coalesce(post_publications.published_at, excluded.published_at),
    updated_at = excluded.updated_at
WHERE post_publications.channel = 'website';

-- ClaimPostPublication takes a social channel for the publishing worker:
-- one that is pending, or stuck publishing since before @stuck_before, of a
-- post still publishing. No row back means there is nothing to do.
-- name: ClaimPostPublication :one
UPDATE post_publications pub
SET status = 'publishing', attempts = pub.attempts + 1, updated_at = @now
FROM posts p
WHERE pub.post_id = @post_id AND pub.channel = @channel AND p.id = pub.post_id
  AND p.status = 'publishing' AND pub.channel <> 'website'
  AND (pub.status = 'pending' OR (pub.status = 'publishing' AND pub.updated_at < @stuck_before))
RETURNING pub.*;

-- FinishPostPublication records how the worker's attempt went.
-- name: FinishPostPublication :one
UPDATE post_publications
SET status = @status, external_id = sqlc.narg(external_id), permalink = sqlc.narg(permalink),
    error = sqlc.narg(error), published_at = sqlc.narg(published_at), updated_at = @now
WHERE post_id = @post_id AND channel = @channel AND status = 'publishing'
RETURNING *;

-- RetryPostPublication gives a failed channel a fresh set of attempts.
-- name: RetryPostPublication :one
UPDATE post_publications
SET status = 'pending', error = NULL, attempts = 0, updated_at = @now
WHERE post_id = @post_id AND channel = @channel AND status = 'failed'
RETURNING *;

-- ListStuckPostPublications finds social channels of publishing posts that
-- have waited or run since before @before: their task was lost.
-- name: ListStuckPostPublications :many
SELECT pub.* FROM post_publications pub
JOIN posts p ON p.id = pub.post_id
WHERE p.status = 'publishing' AND pub.channel <> 'website'
  AND pub.status IN ('pending', 'publishing') AND pub.updated_at < @before
ORDER BY pub.updated_at
LIMIT @max_rows;

-- name: ListDueScheduledPosts :many
SELECT id, scheduled_at FROM posts
WHERE status = 'scheduled' AND scheduled_at < @before::timestamptz
ORDER BY scheduled_at
LIMIT @max_rows;

-- MarkPublicationManual records a channel Daw Mi posted herself; no row back
-- means the channel was not waiting for her.
-- name: MarkPublicationManual :one
UPDATE post_publications
SET status = 'manual', permalink = sqlc.narg(permalink), error = NULL,
    published_at = coalesce(published_at, @now), updated_at = @now
WHERE post_id = @post_id AND channel = @channel AND status IN ('pending', 'failed', 'manual')
RETURNING *;

-- ListPublicArticles selects the website versions that are live: enabled,
-- published on the website, of a post that is publishing or published (not
-- archived, nor back in review after an edit), with the cover image.
-- name: ListPublicArticles :many
SELECT v.slug, p.kind, v.title, v.excerpt, pub.published_at,
       m.id AS cover_id, m.width AS cover_width, m.height AS cover_height,
       m.widths AS cover_widths, m.alt AS cover_alt
FROM post_versions v
JOIN posts p ON p.id = v.post_id
JOIN post_publications pub ON pub.post_id = v.post_id AND pub.channel = 'website'
LEFT JOIN media m ON m.id = v.cover_image_id
WHERE v.channel = 'website' AND v.enabled AND pub.status = 'published'
  AND p.status IN ('publishing', 'published')
  AND (sqlc.narg(kind)::text IS NULL OR p.kind = sqlc.narg(kind))
ORDER BY pub.published_at DESC, v.post_id DESC
LIMIT @max_rows;

-- name: GetPublicArticle :one
SELECT v.slug, p.kind, v.title, v.excerpt, v.body, v.seo_title, v.seo_description, pub.published_at,
       m.id AS cover_id, m.width AS cover_width, m.height AS cover_height,
       m.widths AS cover_widths, m.alt AS cover_alt
FROM post_versions v
JOIN posts p ON p.id = v.post_id
JOIN post_publications pub ON pub.post_id = v.post_id AND pub.channel = 'website'
LEFT JOIN media m ON m.id = v.cover_image_id
WHERE v.channel = 'website' AND v.slug = @slug AND v.enabled
  AND pub.status = 'published' AND p.status IN ('publishing', 'published');
