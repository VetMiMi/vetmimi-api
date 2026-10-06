-- +goose Up
-- Time Daw Mi is unavailable (docs/data-model.md, "Availability"). A block
-- never changes appointments; saving one lists those it overlaps.
CREATE TABLE availability_blocks (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    period     tstzrange NOT NULL,
    all_day    boolean NOT NULL DEFAULT false,
    reason     text,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT availability_blocks_period_not_empty CHECK (NOT isempty(period)),
    CONSTRAINT availability_blocks_reason_length CHECK (char_length(reason) <= 500)
);

CREATE INDEX availability_blocks_period ON availability_blocks USING gist (period);

-- +goose Down
DROP TABLE availability_blocks;
