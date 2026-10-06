-- name: ListServices :many
SELECT * FROM services ORDER BY sort_order, slug;

-- name: GetServiceBySlug :one
SELECT * FROM services WHERE slug = @slug;
