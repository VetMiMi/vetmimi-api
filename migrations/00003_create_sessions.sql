-- +goose Up
-- Administrator sign-in sessions (docs/data-model.md, "Accounts"; ADR-002).
-- The token itself is never stored, only its SHA-256, so the table alone
-- cannot sign anyone in.
CREATE TABLE sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash   bytea NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    -- No updated_at: a session is never edited, and last_seen_at is the
    -- one column that changes.
    created_at   timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT sessions_token_hash_key UNIQUE (token_hash)
);

-- Re-running create-user deletes a user's sessions; the hourly clean-up
-- deletes by expiry.
CREATE INDEX sessions_user_id ON sessions (user_id);
CREATE INDEX sessions_expires_at ON sessions (expires_at);

-- +goose Down
DROP TABLE sessions;
