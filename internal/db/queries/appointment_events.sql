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
