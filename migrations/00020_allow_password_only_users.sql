-- +goose Up
-- A user without a TOTP secret signs in with the password alone, until
-- two-step setup moves into the admin website (issues #143, #142).
ALTER TABLE users ALTER COLUMN totp_secret_enc DROP NOT NULL;

-- +goose Down
-- Fails while any user is password-only; give them a secret first.
ALTER TABLE users ALTER COLUMN totp_secret_enc SET NOT NULL;
