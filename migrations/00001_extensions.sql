-- +goose Up
-- btree_gist lets the appointments exclusion constraint combine an equality
-- on practitioner_id with an overlap check on a tstzrange (ADR-004).
CREATE EXTENSION IF NOT EXISTS btree_gist;
-- pgcrypto supplies gen_random_uuid() for primary keys.
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- +goose Down
DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS btree_gist;
