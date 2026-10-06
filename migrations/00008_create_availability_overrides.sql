-- +goose Up
-- One-off openings and date-specific changes to the weekly hours
-- (docs/data-model.md, "Availability"). Go checks that period lies inside
-- on_date in the practice timezone, which a constraint cannot know.
CREATE TABLE availability_overrides (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    on_date    date NOT NULL,
    kind       text NOT NULL,
    period     tstzrange NOT NULL,
    note       text,
    created_by uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT availability_overrides_kind_check CHECK (kind IN ('open', 'replace')),
    CONSTRAINT availability_overrides_period_not_empty CHECK (NOT isempty(period)),
    CONSTRAINT availability_overrides_note_length CHECK (char_length(note) <= 500)
);

CREATE INDEX availability_overrides_on_date ON availability_overrides (on_date);

-- +goose Down
DROP TABLE availability_overrides;
