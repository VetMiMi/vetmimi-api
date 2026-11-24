-- ListCommunications is every email about an appointment, latest due first,
-- with the appointment's reference and visitor; records of Daw Mi telling the
-- visitor herself stay on the appointment. The page continues after
-- (@after_at, @after_id).
-- name: ListCommunications :many
SELECT c.id, c.appointment_id, c.kind, c.audience, c.channel, c.recipient, c.locale, c.status,
       c.scheduled_for, c.sent_at, c.error, c.attempts, c.resend_of, c.created_at,
       a.reference AS appointment_reference, a.visitor_name
FROM communications c
JOIN appointments a ON a.id = c.appointment_id
WHERE c.channel = 'email'
  AND (sqlc.narg(status)::text IS NULL OR c.status = sqlc.narg(status))
  AND (sqlc.narg(search)::text IS NULL OR a.reference ILIKE sqlc.narg(search))
  AND (sqlc.narg(after_at)::timestamptz IS NULL
       OR (c.scheduled_for, c.id) < (sqlc.narg(after_at), @after_id::uuid))
ORDER BY c.scheduled_for DESC, c.id DESC
LIMIT @max_rows;
