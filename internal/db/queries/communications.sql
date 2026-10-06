-- name: InsertCommunication :one
INSERT INTO communications (
    appointment_id, contact_enquiry_id, kind, audience, recipient, locale, scheduled_for, message
) VALUES (
    @appointment_id, @contact_enquiry_id, @kind, @audience, @recipient, @locale,
    coalesce(sqlc.narg(scheduled_for)::timestamptz, now()), sqlc.narg(message)
)
RETURNING *;

-- name: GetCommunication :one
SELECT * FROM communications WHERE id = @id;

-- LockCommunication skips a row another worker holds, so two workers given
-- the same task never both send it.
-- name: LockCommunication :one
SELECT * FROM communications WHERE id = @id FOR UPDATE SKIP LOCKED;

-- name: SetCommunicationStatus :execrows
UPDATE communications
SET status = @status, error = @error, attempts = @attempts, sent_at = @sent_at,
    provider_message_id = @provider_message_id
WHERE id = @id AND status = 'queued';

-- ListDueCommunications serves comms:sweep through the partial index on
-- queued rows.
-- name: ListDueCommunications :many
SELECT id, kind, scheduled_for FROM communications
WHERE status = 'queued' AND scheduled_for < @before
ORDER BY scheduled_for
LIMIT @max_rows;

-- name: ListQueuedReminderIDs :many
SELECT id FROM communications
WHERE appointment_id = @appointment_id AND kind = 'reminder' AND status = 'queued';

-- RescheduleQueuedReminders moves every queued reminder to its start less
-- @hours, the new reminder_hours.
-- name: RescheduleQueuedReminders :many
UPDATE communications c
SET scheduled_for = a.starts_at - make_interval(hours => @hours::int)
FROM appointments a
WHERE c.appointment_id = a.id AND c.kind = 'reminder' AND c.status = 'queued'
RETURNING c.id, c.scheduled_for;

-- name: GetAppointmentForMessage :one
SELECT a.*, s.name AS service_name, s.fee_text AS service_fee_text,
       s.preparation_text AS service_preparation_text
FROM appointments a
JOIN services s ON s.id = a.service_id
WHERE a.id = @id;

-- PreviousRangeOf is where the latest reschedule moved the appointment from.
-- name: PreviousRangeOf :one
SELECT previous_range FROM appointment_events
WHERE appointment_id = @appointment_id AND kind = 'rescheduled' AND previous_range IS NOT NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- ListAppointmentCommunications is an appointment's messages in the order
-- they were written; rows of one transaction share created_at, so the time
-- each is due orders them, and the id keeps the order stable.
-- name: ListAppointmentCommunications :many
SELECT * FROM communications
WHERE appointment_id = @appointment_id
ORDER BY created_at, scheduled_for, id;
