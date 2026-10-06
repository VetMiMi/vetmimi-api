-- name: InsertAppointmentEvent :exec
INSERT INTO appointment_events (
    appointment_id, kind, from_status, to_status, previous_range, new_range, actor,
    actor_user_id, detail
) VALUES (
    @appointment_id, @kind, sqlc.narg(from_status), sqlc.narg(to_status),
    sqlc.narg(previous_range), sqlc.narg(new_range), @actor, @actor_user_id, @detail
);
