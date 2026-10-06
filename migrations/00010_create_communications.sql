-- +goose Up
-- Every message sent or recorded, written in the transaction that caused it
-- (docs/data-model.md, "communications"; ADR-006). The row is the history;
-- the worker delivers it. contact_enquiry_id gets its foreign key when the
-- contact_enquiries table arrives.
CREATE TABLE communications (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    appointment_id      uuid REFERENCES appointments (id) ON DELETE CASCADE,
    contact_enquiry_id  uuid,
    kind                text NOT NULL,
    audience            text NOT NULL,
    channel             text NOT NULL DEFAULT 'email',
    recipient           text,
    locale              text NOT NULL,
    status              text NOT NULL DEFAULT 'queued',
    scheduled_for       timestamptz NOT NULL DEFAULT now(),
    sent_at             timestamptz,
    provider_message_id text,
    error               text,
    attempts            int NOT NULL DEFAULT 0,
    resend_of           uuid REFERENCES communications (id) ON DELETE SET NULL,
    created_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    note                text,
    created_at          timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT communications_one_parent CHECK (num_nonnulls(appointment_id, contact_enquiry_id) = 1),
    CONSTRAINT communications_kind_check CHECK (kind IN (
        'request_received', 'booking_confirmed', 'request_declined', 'rescheduled', 'cancelled',
        'reminder', 'request_expired', 'practitioner_new_request', 'practitioner_new_booking',
        'practitioner_client_cancelled', 'practitioner_reschedule_requested',
        'practitioner_new_enquiry')),
    CONSTRAINT communications_audience_check CHECK (audience IN ('visitor', 'practitioner')),
    CONSTRAINT communications_channel_check CHECK (channel IN ('email', 'manual')),
    CONSTRAINT communications_locale_check CHECK (locale IN ('en', 'my')),
    CONSTRAINT communications_status_check CHECK (status IN ('queued', 'sent', 'failed', 'cancelled')),
    CONSTRAINT communications_sent_has_time CHECK (status <> 'sent' OR sent_at IS NOT NULL),
    CONSTRAINT communications_email_has_recipient CHECK (channel = 'manual' OR recipient IS NOT NULL),
    CONSTRAINT communications_recipient_lower CHECK (recipient = lower(recipient)),
    CONSTRAINT communications_note_manual CHECK (note IS NULL OR channel = 'manual'),
    CONSTRAINT communications_lengths CHECK (
        char_length(recipient) <= 254 AND char_length(error) <= 200 AND char_length(note) <= 2000)
);

CREATE INDEX communications_appointment_created ON communications (appointment_id, created_at);
CREATE INDEX communications_queued_scheduled ON communications (scheduled_for) WHERE status = 'queued';
CREATE INDEX communications_failed_created ON communications (created_at) WHERE status = 'failed';

-- +goose Down
DROP TABLE communications;
