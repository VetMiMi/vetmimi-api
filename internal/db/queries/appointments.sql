-- InsertAppointment does nothing on a reference collision, so the caller can
-- retry with a new reference inside the same transaction. Every other
-- violation, appointments_no_overlap included, is an error.
-- name: InsertAppointment :one
INSERT INTO appointments (
    reference, practitioner_id, service_id, status, starts_at, ends_at, duration_minutes,
    busy_range, timezone, format, locale, source, visitor_name, visitor_email, visitor_phone,
    visitor_note, privacy_ack_at, policy_ack_at, hold_expires_at, management_token_seed,
    management_token_hash, created_by, admin_note
) VALUES (
    @reference, @practitioner_id, @service_id, @status, @starts_at, @ends_at, @duration_minutes,
    @busy_range, @timezone, @format, @locale, @source, @visitor_name, @visitor_email,
    sqlc.narg(visitor_phone), sqlc.narg(visitor_note), @privacy_ack_at, @policy_ack_at,
    @hold_expires_at, @management_token_seed, @management_token_hash, @created_by,
    sqlc.narg(admin_note)
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

-- name: LockAppointment :one
SELECT * FROM appointments WHERE id = @id FOR UPDATE;

-- SetAppointmentStatus is the one write of a status change; the caller has
-- checked it with booking.CanTransition. Only a pending or expired request
-- keeps its hold.
-- name: SetAppointmentStatus :one
UPDATE appointments
SET status = @status, status_changed_at = @now, updated_at = @now, version = version + 1,
    hold_expires_at = CASE WHEN @status IN ('pending', 'expired') THEN hold_expires_at END
WHERE id = @id
RETURNING *;

-- MoveAppointment is a reschedule's one write (ADR-004): the new time is
-- taken in the same statement that releases the old, so an overlap refuses
-- the whole move. A pending hold never outlasts the new start.
-- name: MoveAppointment :one
UPDATE appointments
SET starts_at = @starts_at, ends_at = @ends_at, busy_range = @busy_range,
    hold_expires_at = CASE WHEN status = 'pending' THEN least(hold_expires_at, @starts_at) END,
    updated_at = @now, version = version + 1
WHERE id = @id
RETURNING *;

-- name: SetAppointmentNote :one
UPDATE appointments
SET admin_note = sqlc.narg(admin_note), updated_at = @now, version = version + 1
WHERE id = @id
RETURNING *;

-- name: GetAppointmentDetail :one
SELECT a.*, s.slug AS service_slug, s.name AS service_name
FROM appointments a
JOIN services s ON s.id = a.service_id
WHERE a.id = @id;

-- ListAppointments serves the admin list (booking.ListAppointments). sort_at
-- is the hold's end for the pending view and the start otherwise; the page
-- continues after (@after_at, @after_id) in the list's direction. @past
-- keeps rows that started before it or are final.
-- name: ListAppointments :many
WITH listed AS (
    SELECT a.id, a.reference, a.status, a.starts_at, a.ends_at, a.duration_minutes, a.timezone,
           a.format, a.source, a.visitor_name, a.hold_expires_at, a.created_at, a.updated_at,
           a.service_id,
           CASE WHEN @by_hold::bool THEN coalesce(a.hold_expires_at, a.starts_at)
                ELSE a.starts_at END AS sort_at
    FROM appointments a
    WHERE a.status = ANY(@statuses::text[])
      AND (sqlc.narg(starts_from)::timestamptz IS NULL OR a.starts_at >= sqlc.narg(starts_from))
      AND (sqlc.narg(starts_before)::timestamptz IS NULL OR a.starts_at < sqlc.narg(starts_before))
      AND (sqlc.narg(past)::timestamptz IS NULL OR a.starts_at < sqlc.narg(past)
           OR a.status NOT IN ('pending', 'confirmed'))
      AND (sqlc.narg(service_id)::uuid IS NULL OR a.service_id = sqlc.narg(service_id))
      AND (sqlc.narg(format)::text IS NULL OR a.format = sqlc.narg(format))
      AND (sqlc.narg(search)::text IS NULL OR a.reference ILIKE sqlc.narg(search)
           OR a.visitor_name ILIKE sqlc.narg(search) OR a.visitor_email ILIKE sqlc.narg(search))
)
SELECT l.id, l.reference, l.status, l.starts_at, l.ends_at, l.duration_minutes, l.timezone,
       l.format, l.source, l.visitor_name, l.hold_expires_at, l.created_at, l.updated_at,
       l.sort_at::timestamptz AS sort_at,
       s.id AS service_id, s.slug AS service_slug, s.name AS service_name
FROM listed l
JOIN services s ON s.id = l.service_id
WHERE sqlc.narg(after_at)::timestamptz IS NULL
   OR (@ascending::bool AND (l.sort_at, l.id) > (sqlc.narg(after_at), @after_id::uuid))
   OR (NOT @ascending::bool AND (l.sort_at, l.id) < (sqlc.narg(after_at), @after_id::uuid))
ORDER BY CASE WHEN @ascending::bool THEN l.sort_at END,
         CASE WHEN @ascending::bool THEN l.id END,
         CASE WHEN NOT @ascending::bool THEN l.sort_at END DESC,
         CASE WHEN NOT @ascending::bool THEN l.id END DESC
LIMIT @max_rows;

-- ListOverdueHolds serves booking:sweep-holds through the partial index on
-- pending holds.
-- name: ListOverdueHolds :many
SELECT id FROM appointments
WHERE status = 'pending' AND hold_expires_at <= @now::timestamptz
ORDER BY hold_expires_at
LIMIT @max_rows;
