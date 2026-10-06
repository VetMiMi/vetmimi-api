-- InsertContactEnquiry does nothing on a reference collision, so the caller
-- can retry with a new reference inside the same transaction.
-- name: InsertContactEnquiry :one
INSERT INTO contact_enquiries (
    reference, name, email, organisation, subject, enquiry_type, service_id, message, locale,
    privacy_ack_at, created_at
) VALUES (
    @reference, @name, @email, sqlc.narg(organisation), sqlc.narg(subject), @enquiry_type,
    @service_id, @message, @locale, @privacy_ack_at, @privacy_ack_at
)
ON CONFLICT (reference) DO NOTHING
RETURNING *;

-- name: GetContactEnquiry :one
SELECT e.*, s.slug AS service_slug, s.name AS service_name
FROM contact_enquiries e
LEFT JOIN services s ON s.id = e.service_id
WHERE e.id = @id;

-- ListContactEnquiries is newest first; the page continues after
-- (@after_at, @after_id).
-- name: ListContactEnquiries :many
SELECT e.*, s.slug AS service_slug, s.name AS service_name
FROM contact_enquiries e
LEFT JOIN services s ON s.id = e.service_id
WHERE (sqlc.narg(status)::text IS NULL OR e.status = sqlc.narg(status))
  AND (sqlc.narg(search)::text IS NULL OR e.reference ILIKE sqlc.narg(search)
       OR e.name ILIKE sqlc.narg(search) OR e.email ILIKE sqlc.narg(search)
       OR e.organisation ILIKE sqlc.narg(search) OR e.subject ILIKE sqlc.narg(search))
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (e.created_at, e.id) < (sqlc.narg(after_at), @after_id::uuid))
ORDER BY e.created_at DESC, e.id DESC
LIMIT @max_rows;

-- MarkContactEnquiryHandled leaves a handled enquiry as it is.
-- name: MarkContactEnquiryHandled :execrows
UPDATE contact_enquiries
SET status = 'handled', handled_at = @now::timestamptz, handled_by = @handled_by
WHERE id = @id AND status = 'new';

-- DeleteRetainedContactEnquiries deletes up to @max_rows enquiries created
-- before @before, whatever their status; their communications go with them.
-- name: DeleteRetainedContactEnquiries :execrows
DELETE FROM contact_enquiries
WHERE id IN (
    SELECT e.id FROM contact_enquiries e WHERE e.created_at < @before LIMIT @max_rows
);
