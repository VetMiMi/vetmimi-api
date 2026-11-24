-- name: SetAppointmentMeetingLink :one
UPDATE appointments
SET meeting_link = @meeting_link::text, updated_at = @now, version = version + 1
WHERE id = @id
RETURNING *;
