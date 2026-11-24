-- ExportAppointments leaves out the admin note: it is private to Daw Mi.
-- name: ExportAppointments :many
SELECT a.reference, a.status, (s.name->>'en')::text AS service_name, a.starts_at, a.duration_minutes,
       a.format, a.visitor_name, a.visitor_email, a.visitor_phone, a.visitor_note, a.late_cancellation,
       a.created_at, a.updated_at
FROM appointments a
JOIN services s ON s.id = a.service_id
WHERE a.starts_at >= @starts_from::timestamptz AND a.starts_at < @starts_before::timestamptz
ORDER BY a.starts_at, a.id;
