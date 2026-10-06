-- name: InsertVideoRoom :one
INSERT INTO video_rooms (appointment_id, join_token_seed, join_token_hash, opens_at, closes_at, created_at, updated_at)
VALUES (@appointment_id, @join_token_seed, @join_token_hash, @opens_at, @closes_at, @now, @now)
RETURNING *;

-- name: GetVideoRoomByAppointment :one
SELECT * FROM video_rooms WHERE appointment_id = @appointment_id;

-- GetVideoSessionByTokenHash is a room with what its join page shows of the
-- appointment: times, status, locale and the service's public name.
-- name: GetVideoSessionByTokenHash :one
SELECT r.id, r.state, r.opens_at, r.closes_at,
       a.status AS appointment_status, a.starts_at, a.ends_at, a.timezone, a.locale,
       s.slug AS service_slug, s.name AS service_name
FROM video_rooms r
JOIN appointments a ON a.id = r.appointment_id
JOIN services s ON s.id = a.service_id
WHERE r.join_token_hash = @join_token_hash;

-- MoveVideoRoom follows a reschedule; an ended room stays as it is.
-- name: MoveVideoRoom :many
UPDATE video_rooms SET opens_at = @opens_at, closes_at = @closes_at, updated_at = @now
WHERE appointment_id = @appointment_id AND state <> 'ended'
RETURNING *;

-- EndVideoRoom ends an appointment's room for a reason; a room already
-- ended is left as it is, so of two racing ends exactly one writes.
-- name: EndVideoRoom :one
UPDATE video_rooms SET state = 'ended', ended_at = @now::timestamptz, ended_reason = @ended_reason, updated_at = @now
WHERE appointment_id = @appointment_id AND state <> 'ended'
RETURNING *;

-- CloseVideoRoomIfDue ends a room whose window has passed; a room moved
-- later, or already ended, is left alone.
-- name: CloseVideoRoomIfDue :exec
UPDATE video_rooms SET state = 'ended', ended_at = @now::timestamptz, ended_reason = 'window_closed', updated_at = @now
WHERE id = @id AND state <> 'ended' AND closes_at <= @now;

-- CloseOverdueVideoRooms ends every room still open past its window, for the
-- sweep that catches close-room tasks Redis lost.
-- name: CloseOverdueVideoRooms :execrows
UPDATE video_rooms SET state = 'ended', ended_at = @now::timestamptz, ended_reason = 'window_closed', updated_at = @now
WHERE state <> 'ended' AND closes_at <= @now;

-- name: GetVideoRoom :one
SELECT * FROM video_rooms WHERE id = @id;

-- ListVideoRooms reads the rooms the hub holds sockets for.
-- name: ListVideoRooms :many
SELECT * FROM video_rooms WHERE id = ANY(@ids::uuid[]);

-- SetVideoRoomInSession marks a waiting room in session once both
-- participants have joined; started_at keeps the first time.
-- name: SetVideoRoomInSession :execrows
UPDATE video_rooms SET state = 'in_session', started_at = COALESCE(started_at, @now::timestamptz), updated_at = @now
WHERE id = @id AND state = 'waiting';

-- name: GetAppointmentStatus :one
SELECT status FROM appointments WHERE id = @id;
