-- InsertAppointment does nothing on a reference collision, so the caller can
-- retry with a new reference inside the same transaction. Every other
-- violation, appointments_no_overlap included, is an error.
-- name: InsertAppointment :one
INSERT INTO appointments (
    reference, practitioner_id, service_id, status, starts_at, ends_at, duration_minutes,
    busy_range, timezone, format, locale, source, visitor_name, visitor_email, visitor_phone,
    visitor_note, privacy_ack_at, policy_ack_at, hold_expires_at, management_token_seed,
    management_token_hash, created_by
) VALUES (
    @reference, @practitioner_id, @service_id, @status, @starts_at, @ends_at, @duration_minutes,
    @busy_range, @timezone, @format, @locale, @source, @visitor_name, @visitor_email,
    sqlc.narg(visitor_phone), sqlc.narg(visitor_note), @privacy_ack_at, @policy_ack_at,
    @hold_expires_at, @management_token_seed, @management_token_hash, @created_by
)
ON CONFLICT (reference) DO NOTHING
RETURNING *;

-- LockSchedule takes the transaction-scoped lock that availability writes and
-- appointment creation share (docs/architecture.md, walkthrough 1, step 5),
-- keyed by the one practitioner.
-- name: LockSchedule :exec
SELECT pg_advisory_xact_lock(hashtext(
    'availability:' || coalesce((SELECT id::text FROM users WHERE is_practitioner), '')));

-- name: ListOverlappingAppointments :many
SELECT a.id, a.reference, a.status, a.starts_at, a.ends_at, a.duration_minutes, a.timezone,
       a.format, a.source, a.visitor_name, a.hold_expires_at, a.created_at, a.updated_at,
       s.id AS service_id, s.slug AS service_slug, s.name AS service_name
FROM appointments a
JOIN services s ON s.id = a.service_id
WHERE a.status IN ('pending', 'confirmed') AND a.busy_range && @period::tstzrange
ORDER BY a.starts_at;
