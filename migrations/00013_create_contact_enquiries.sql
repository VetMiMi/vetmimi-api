-- +goose Up
-- Messages from the contact form and enquiry-only services
-- (docs/data-model.md, "contact_enquiries").
CREATE TABLE contact_enquiries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    reference      text NOT NULL,
    name           text NOT NULL,
    email          text NOT NULL,
    organisation   text,
    subject        text,
    enquiry_type   text NOT NULL,
    service_id     uuid REFERENCES services (id) ON DELETE SET NULL,
    message        text NOT NULL,
    locale         text NOT NULL DEFAULT 'en',
    privacy_ack_at timestamptz NOT NULL,
    status         text NOT NULL DEFAULT 'new',
    handled_at     timestamptz,
    handled_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT contact_enquiries_reference_key UNIQUE (reference),
    CONSTRAINT contact_enquiries_enquiry_type_check CHECK (enquiry_type IN (
        'collaboration', 'workshop', 'speaking', 'art_of_wellness', 'media', 'organisation', 'general')),
    CONSTRAINT contact_enquiries_locale_check CHECK (locale IN ('en', 'my')),
    CONSTRAINT contact_enquiries_status_check CHECK (status IN ('new', 'handled')),
    CONSTRAINT contact_enquiries_email_lower CHECK (email = lower(email)),
    CONSTRAINT contact_enquiries_lengths CHECK (
        char_length(name) BETWEEN 1 AND 120
        AND char_length(email) <= 254
        AND char_length(organisation) <= 200
        AND char_length(subject) <= 200
        AND char_length(message) BETWEEN 1 AND 5000)
);

CREATE INDEX contact_enquiries_status_created ON contact_enquiries (status, created_at);

ALTER TABLE communications ADD CONSTRAINT communications_contact_enquiry_id_fkey
    FOREIGN KEY (contact_enquiry_id) REFERENCES contact_enquiries (id) ON DELETE CASCADE;
CREATE INDEX communications_contact_enquiry ON communications (contact_enquiry_id)
    WHERE contact_enquiry_id IS NOT NULL;

-- +goose Down
ALTER TABLE communications DROP CONSTRAINT communications_contact_enquiry_id_fkey;
DROP INDEX communications_contact_enquiry;
DROP TABLE contact_enquiries;
