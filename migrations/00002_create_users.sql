-- +goose Up
-- Administrators (docs/data-model.md, "Accounts"). Created and re-enrolled by
-- `api --mode create-user`; the API has no enrolment routes.
CREATE TABLE users (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email           text NOT NULL,
    display_name    text NOT NULL,
    password_hash   text NOT NULL,
    roles           text[] NOT NULL,
    is_practitioner boolean NOT NULL DEFAULT false,
    totp_secret_enc bytea NOT NULL,
    totp_last_step  bigint,
    last_sign_in_at timestamptz,
    disabled_at     timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT users_email_key UNIQUE (email),
    -- Sign-in looks emails up lower-case; a mixed-case row could never sign in.
    CONSTRAINT users_email_lower_case CHECK (email = lower(email)),
    CONSTRAINT users_roles_check CHECK (
        roles <@ ARRAY['content_editor', 'booking_admin', 'site_admin']::text[]
        AND cardinality(roles) > 0
    )
);

-- Appointments are booked with the one practitioner, Daw Mi.
CREATE UNIQUE INDEX users_one_practitioner ON users (is_practitioner) WHERE is_practitioner;

-- +goose Down
DROP TABLE users;
