-- +goose Up
-- Recurring weekly hours (docs/data-model.md, "Availability"). The one
-- wall-clock value in the system: weekday plus local practice time, never
-- converted to UTC; slot generation applies the timezone per date.
CREATE TABLE availability_rules (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    weekday    smallint NOT NULL,
    start_time time NOT NULL,
    end_time   time NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT availability_rules_weekday_check CHECK (weekday BETWEEN 1 AND 7),
    CONSTRAINT availability_rules_end_after_start CHECK (end_time > start_time),
    -- Periods on one weekday may touch but not overlap. The times are put on
    -- an arbitrary date because time has no range type.
    CONSTRAINT availability_rules_no_overlap EXCLUDE USING gist (
        weekday WITH =,
        tsrange(date '2000-01-01' + start_time, date '2000-01-01' + end_time) WITH &&)
);

-- +goose Down
DROP TABLE availability_rules;
