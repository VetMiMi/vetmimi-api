-- +goose Up
-- Stored responses for retried creates, kept 24 hours (docs/data-model.md,
-- "idempotency_keys"; ADR-004). The row is inserted in the creating
-- transaction, so a create that rolls back leaves no key behind.
CREATE TABLE idempotency_keys (
    scope           text NOT NULL,
    key             text NOT NULL,
    request_hash    bytea NOT NULL,
    resource_id     uuid,
    response_status smallint,
    response_body   jsonb,
    created_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (scope, key),
    CONSTRAINT idempotency_keys_scope_check
        CHECK (scope IN ('public_appointment', 'admin_appointment', 'contact_enquiry')),
    CONSTRAINT idempotency_keys_key_length CHECK (char_length(key) BETWEEN 1 AND 128)
);

CREATE INDEX idempotency_keys_created_at ON idempotency_keys (created_at);

-- +goose Down
DROP TABLE idempotency_keys;
