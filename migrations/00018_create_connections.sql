-- +goose Up
-- Platform connections for the publishing portal (docs/data-model.md,
-- "Connections"): one row per platform. The token is sealed with AES-GCM
-- under TOTP_ENCRYPTION_KEY: the long-lived user token while Daw Mi picks
-- her Page, the Page token once connected.
CREATE TABLE connections (
    platform           text PRIMARY KEY,
    status             text NOT NULL,
    token              bytea NOT NULL,
    account_id         text,
    account_name       text,
    instagram_id       text,
    instagram_username text,
    expires_at         timestamptz,
    last_error         text,
    connected_by       uuid REFERENCES users (id) ON DELETE SET NULL,
    connected_at       timestamptz NOT NULL,
    updated_at         timestamptz NOT NULL,

    CONSTRAINT connections_platform_check CHECK (platform IN ('meta')),
    CONSTRAINT connections_status_check CHECK (status IN ('choosing_page', 'connected')),
    CONSTRAINT connections_account_when_connected CHECK (status <> 'connected' OR account_id IS NOT NULL)
);

-- +goose Down
DROP TABLE connections;
