-- +goose Up
-- Business rules Daw Mi may change without a deploy (docs/data-model.md,
-- "settings" and "Settings keys"). platform/settings types and range-checks
-- each value. The defaults are provisional: Daw Mi changes them from admin,
-- and DO NOTHING keeps her changes if this insert ever runs again.
CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_by uuid REFERENCES users (id) ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO settings (key, value) VALUES
    ('timezone', '"Australia/Sydney"'),
    ('booking_mode', '"request_approval"'),
    ('public_booking_enabled', 'true'),
    ('pending_hold_hours', '48'),
    ('reminder_hours', '24'),
    ('min_notice_hours', '24'),
    ('max_advance_days', '60'),
    ('slot_step_minutes', '30'),
    ('cancellation_notice_hours', '48'),
    ('late_cancellation_fee_percent', '50'),
    ('late_cancellation_first_waived', 'true'),
    ('no_show_fee_percent', '100'),
    ('meeting_link_mode', '"vetmimi_room"'),
    ('payment_methods', '["bank_transfer", "card"]'),
    ('invoice_timing', '"after_session"'),
    ('retention_months', '24'),
    ('contact_email', '"meenaerie@gmail.com"'),
    ('response_time', '{"en": "Usually within 2 business days"}')
ON CONFLICT (key) DO NOTHING;

-- +goose Down
DROP TABLE settings;
