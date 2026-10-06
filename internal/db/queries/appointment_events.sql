-- name: InsertAppointmentEvent :exec
INSERT INTO appointment_events (
    appointment_id, kind, from_status, to_status, previous_range, new_range, actor,
    actor_user_id, detail
) VALUES (
    @appointment_id, @kind, sqlc.narg(from_status), sqlc.narg(to_status),
    sqlc.narg(previous_range), sqlc.narg(new_range), @actor, @actor_user_id, @detail
);

-- name: ListAppointmentEvents :many
SELECT e.*, u.display_name AS actor_name
FROM appointment_events e
LEFT JOIN users u ON u.id = e.actor_user_id
WHERE e.appointment_id = @appointment_id
ORDER BY e.created_at, e.id;

-- RescheduleRequestOpen reports a reschedule request newer than the last
-- reschedule or status change, which would have answered it.
-- name: RescheduleRequestOpen :one
SELECT EXISTS (
    SELECT 1 FROM appointment_events r
    WHERE r.appointment_id = @appointment_id AND r.kind = 'reschedule_requested'
      AND r.id > coalesce((
          SELECT max(e.id) FROM appointment_events e
          WHERE e.appointment_id = @appointment_id AND (e.kind = 'rescheduled' OR e.to_status IS NOT NULL)
      ), 0)
)::bool;

-- LatestRescheduleRequest is the detail of the visitor's newest request.
-- name: LatestRescheduleRequest :one
SELECT detail FROM appointment_events
WHERE appointment_id = @appointment_id AND kind = 'reschedule_requested'
ORDER BY id DESC
LIMIT 1;
