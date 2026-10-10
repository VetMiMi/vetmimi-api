-- name: DashboardCounts :one
SELECT count(*) FILTER (WHERE status = 'pending') AS pending,
       count(*) FILTER (WHERE status = 'confirmed' AND starts_at >= @now::timestamptz) AS confirmed,
       count(*) FILTER (WHERE starts_at >= @day_start::timestamptz AND starts_at < @day_end::timestamptz) AS today,
       count(*) FILTER (WHERE starts_at >= @week_start::timestamptz AND starts_at < @week_end::timestamptz) AS this_week
FROM appointments
WHERE status IN ('pending', 'confirmed');

-- ListAttention returns every attention item in dashboard order; rank orders the kinds and the Go side
-- writes each item's detail. A failed message counts until a later message of its kind is sent.
-- name: ListAttention :many
WITH items AS (
    SELECT 1 AS rank, 'pending_request' AS kind, a.id, coalesce(a.hold_expires_at, a.starts_at) AS sort_at,
           ''::text AS communication_kind
    FROM appointments a
    WHERE a.status = 'pending'
    UNION ALL
    SELECT 2, 'hold_expiring', a.id, a.hold_expires_at, ''
    FROM appointments a
    WHERE a.status = 'pending' AND a.hold_expires_at <= @hold_horizon::timestamptz
    UNION ALL
    SELECT DISTINCT 3, 'failed_communication', a.id, a.starts_at, f.kind
    FROM communications f
    JOIN appointments a ON a.id = f.appointment_id
    WHERE f.status = 'failed'
      AND NOT EXISTS (
          SELECT 1 FROM communications s
          WHERE s.appointment_id = f.appointment_id AND s.kind = f.kind AND s.status = 'sent'
            AND s.created_at > f.created_at)
    UNION ALL
    SELECT 4, 'reschedule_requested', a.id, a.starts_at, ''
    FROM appointments a
    WHERE a.status IN ('pending', 'confirmed')
      AND EXISTS (
          SELECT 1 FROM appointment_events r
          WHERE r.appointment_id = a.id AND r.kind = 'reschedule_requested'
            AND r.id > coalesce((
                SELECT max(e.id) FROM appointment_events e
                WHERE e.appointment_id = a.id AND (e.kind = 'rescheduled' OR e.to_status IS NOT NULL)
            ), 0))
    UNION ALL
    SELECT 5, 'completion_due', a.id, a.starts_at, ''
    FROM appointments a
    WHERE a.status = 'confirmed' AND a.ends_at < @now::timestamptz
    UNION ALL
    SELECT 6, 'block_conflict', a.id, a.starts_at, ''
    FROM appointments a
    WHERE a.status IN ('pending', 'confirmed')
      AND EXISTS (SELECT 1 FROM availability_blocks b WHERE b.period && a.busy_range)
)
SELECT i.kind::text AS kind, i.communication_kind::text AS communication_kind,
       a.id, a.reference, a.starts_at, a.hold_expires_at
FROM items i
JOIN appointments a ON a.id = i.id
ORDER BY i.rank, i.sort_at, a.id;
