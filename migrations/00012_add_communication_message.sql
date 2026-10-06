-- +goose Up
-- Daw Mi's own words to the visitor on a decline or a cancellation, kept on
-- the row the worker renders, so a retry or a resend says the same thing.
ALTER TABLE communications
    ADD COLUMN message text,
    ADD CONSTRAINT communications_message_length CHECK (char_length(message) <= 1000);

-- +goose Down
ALTER TABLE communications DROP COLUMN message;
