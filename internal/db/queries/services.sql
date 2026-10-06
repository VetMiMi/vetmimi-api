-- name: ListServices :many
SELECT * FROM services ORDER BY sort_order, name->>'en';

-- name: GetService :one
SELECT * FROM services WHERE id = @id;

-- name: GetServiceBySlug :one
SELECT * FROM services WHERE slug = @slug;

-- name: CreateService :one
INSERT INTO services (
    slug, name, description, booking_action, duration_minutes, buffer_before_minutes,
    buffer_after_minutes, formats, fee_text, preparation_text, sort_order
) VALUES (
    @slug, @name, @description, @booking_action, @duration_minutes, @buffer_before_minutes,
    @buffer_after_minutes, @formats, @fee_text, @preparation_text, @sort_order
)
RETURNING *;

-- UpdateService writes the whole row only if nobody changed it since the
-- caller read version; no row back means they did.
-- name: UpdateService :one
UPDATE services
SET slug = @slug, name = @name, description = @description, booking_action = @booking_action,
    state = @state, duration_minutes = @duration_minutes,
    buffer_before_minutes = @buffer_before_minutes, buffer_after_minutes = @buffer_after_minutes,
    formats = @formats, fee_text = @fee_text, preparation_text = @preparation_text,
    sort_order = @sort_order, version = version + 1, updated_at = @now
WHERE id = @id AND version = @version
RETURNING *;

-- name: DeleteService :execrows
DELETE FROM services WHERE id = @id;
