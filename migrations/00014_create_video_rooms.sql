-- +goose Up
-- A VetMiMi video room for one confirmed online appointment
-- (docs/data-model.md, "video_rooms"; ADR-007).
CREATE TABLE video_rooms (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    appointment_id  uuid NOT NULL REFERENCES appointments (id) ON DELETE CASCADE,
    join_token_seed bytea NOT NULL,
    join_token_hash bytea NOT NULL,
    opens_at        timestamptz NOT NULL,
    closes_at       timestamptz NOT NULL,
    state           text NOT NULL DEFAULT 'waiting',
    started_at      timestamptz,
    ended_at        timestamptz,
    ended_reason    text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT video_rooms_appointment_id_key UNIQUE (appointment_id),
    CONSTRAINT video_rooms_join_token_hash_key UNIQUE (join_token_hash),
    CONSTRAINT video_rooms_state_check CHECK (state IN ('waiting', 'in_session', 'ended')),
    CONSTRAINT video_rooms_ended_reason_check CHECK (
        ended_reason IN ('practitioner', 'window_closed', 'appointment_cancelled'))
);

-- +goose Down
DROP TABLE video_rooms;
