-- +goose Up
-- One row per request or booking, whatever happens to it (docs/data-model.md,
-- "Appointments"; ADR-004).
CREATE TABLE appointments (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reference             text NOT NULL,
    practitioner_id       uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    service_id            uuid NOT NULL REFERENCES services (id) ON DELETE RESTRICT,
    status                text NOT NULL DEFAULT 'pending',
    starts_at             timestamptz NOT NULL,
    ends_at               timestamptz NOT NULL,
    duration_minutes      int NOT NULL,
    busy_range            tstzrange NOT NULL,
    timezone              text NOT NULL,
    format                text NOT NULL,
    locale                text NOT NULL DEFAULT 'en',
    source                text NOT NULL DEFAULT 'website',
    visitor_name          text NOT NULL,
    visitor_email         text NOT NULL,
    visitor_phone         text,
    visitor_note          text,
    privacy_ack_at        timestamptz,
    policy_ack_at         timestamptz,
    hold_expires_at       timestamptz,
    management_token_seed bytea NOT NULL,
    management_token_hash bytea NOT NULL,
    meeting_link          text,
    admin_note            text,
    late_cancellation     boolean NOT NULL DEFAULT false,
    status_changed_at     timestamptz NOT NULL DEFAULT now(),
    created_by            uuid REFERENCES users (id) ON DELETE SET NULL,
    version               int NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT appointments_reference_key UNIQUE (reference),
    CONSTRAINT appointments_management_token_hash_key UNIQUE (management_token_hash),
    CONSTRAINT appointments_status_check CHECK (status IN (
        'pending', 'confirmed', 'declined', 'expired', 'cancelled_by_client',
        'cancelled_by_practitioner', 'completed', 'no_show')),
    CONSTRAINT appointments_format_check CHECK (format IN ('online', 'in_person')),
    CONSTRAINT appointments_locale_check CHECK (locale IN ('en', 'my')),
    CONSTRAINT appointments_source_check CHECK (source IN ('website', 'manual')),
    CONSTRAINT appointments_ends_after_starts CHECK (ends_at > starts_at),
    CONSTRAINT appointments_busy_range_covers CHECK (busy_range @> tstzrange(starts_at, ends_at)),
    CONSTRAINT appointments_hold_while_pending
        CHECK (hold_expires_at IS NULL OR status IN ('pending', 'expired')),
    CONSTRAINT appointments_website_acknowledged
        CHECK (source = 'manual' OR (privacy_ack_at IS NOT NULL AND policy_ack_at IS NOT NULL)),
    CONSTRAINT appointments_visitor_lengths CHECK (
        char_length(visitor_name) BETWEEN 1 AND 120
        AND char_length(visitor_email) <= 254
        AND char_length(visitor_phone) <= 32
        AND char_length(visitor_note) <= 500
        AND char_length(admin_note) <= 2000),
    CONSTRAINT appointments_meeting_link_https CHECK (meeting_link LIKE 'https://%')
);

-- The guard against double booking (ADR-004): no two pending or confirmed
-- appointments of one practitioner may overlap, buffers included. Any other
-- status falls out of the predicate, so a cancelled slot reopens.
ALTER TABLE appointments ADD CONSTRAINT appointments_no_overlap
    EXCLUDE USING gist (practitioner_id WITH =, busy_range WITH &&)
    WHERE (status IN ('pending', 'confirmed'));

CREATE INDEX appointments_status_starts_at ON appointments (status, starts_at);
CREATE INDEX appointments_service_starts_at ON appointments (service_id, starts_at);
CREATE INDEX appointments_visitor_email ON appointments (visitor_email);
CREATE INDEX appointments_hold_expires_at ON appointments (hold_expires_at) WHERE status = 'pending';

-- Appointment history; append-only. detail holds non-personal facts only.
CREATE TABLE appointment_events (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    appointment_id uuid NOT NULL REFERENCES appointments (id) ON DELETE CASCADE,
    kind           text NOT NULL,
    from_status    text,
    to_status      text,
    previous_range tstzrange,
    new_range      tstzrange,
    actor          text NOT NULL,
    actor_user_id  uuid REFERENCES users (id) ON DELETE SET NULL,
    detail         jsonb NOT NULL DEFAULT '{}',
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT appointment_events_kind_check CHECK (kind IN (
        'created', 'confirmed', 'declined', 'rescheduled', 'reschedule_requested', 'cancelled',
        'completed', 'no_show', 'expired', 'note_updated', 'meeting_link_set')),
    CONSTRAINT appointment_events_actor_check CHECK (actor IN ('visitor', 'admin', 'system')),
    CONSTRAINT appointment_events_detail_object CHECK (jsonb_typeof(detail) = 'object')
);

CREATE INDEX appointment_events_appointment_created ON appointment_events (appointment_id, created_at);

-- +goose Down
DROP TABLE appointment_events;
DROP TABLE appointments;
