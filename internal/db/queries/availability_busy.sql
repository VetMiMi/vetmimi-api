-- ListBusyPeriods is the time slot generation must keep free inside within:
-- every block and every pending or confirmed appointment's busy range. It
-- selects the periods alone, never a reason or anything about a visitor.
-- name: ListBusyPeriods :many
SELECT busy_range AS period FROM appointments
WHERE status IN ('pending', 'confirmed') AND busy_range && @within::tstzrange
UNION ALL
SELECT period FROM availability_blocks
WHERE period && @within::tstzrange;
