-- +goose Up
-- Text per locale (docs/data-model.md, "Conventions"): an object whose only
-- keys are en and my. Columns that need English add CHECK (col ? 'en').
CREATE DOMAIN localized AS jsonb
    CHECK (jsonb_typeof(VALUE) = 'object' AND VALUE - 'en' - 'my' = '{}'::jsonb);

-- What can be booked or enquired about (docs/data-model.md, "services").
CREATE TABLE services (
    id                    uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                  text NOT NULL,
    name                  localized NOT NULL,
    description           localized,
    booking_action        text NOT NULL DEFAULT 'request',
    state                 text NOT NULL DEFAULT 'active',
    duration_minutes      int,
    buffer_before_minutes int NOT NULL DEFAULT 0,
    buffer_after_minutes  int NOT NULL DEFAULT 0,
    formats               text[] NOT NULL DEFAULT '{online}',
    fee_text              localized,
    preparation_text      localized,
    sort_order            int NOT NULL DEFAULT 0,
    version               int NOT NULL DEFAULT 1,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT services_slug_key UNIQUE (slug),
    CONSTRAINT services_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    CONSTRAINT services_name_en CHECK (name ? 'en'),
    CONSTRAINT services_booking_action_check
        CHECK (booking_action IN ('book', 'request', 'enquiry_only', 'not_bookable')),
    CONSTRAINT services_state_check CHECK (state IN ('active', 'paused', 'archived')),
    CONSTRAINT services_duration_range CHECK (duration_minutes BETWEEN 5 AND 480),
    CONSTRAINT services_buffer_range CHECK (
        buffer_before_minutes BETWEEN 0 AND 240 AND buffer_after_minutes BETWEEN 0 AND 240
    ),
    CONSTRAINT services_formats_check CHECK (
        formats <@ ARRAY['online', 'in_person']::text[] AND cardinality(formats) > 0
    ),
    -- A slot is only as long as its service, so a bookable service needs one.
    CONSTRAINT services_duration_required
        CHECK (booking_action NOT IN ('book', 'request') OR duration_minutes IS NOT NULL)
);

-- The four launch services, names from the site's messages/*/services.json.
-- Durations and buffers are provisional (Booking & Admin UX, section 40).
-- free-consultation is in data-model.md but nowhere on the site or in the
-- requirements, so it starts paused until Daw Mi confirms it. DO NOTHING keeps
-- admin edits if this insert ever runs again.
INSERT INTO services (slug, name, booking_action, state, duration_minutes,
                      buffer_before_minutes, buffer_after_minutes, sort_order) VALUES
    ('individual-art-therapy',
     '{"en": "Individual Art Therapy", "my": "တစ်ဦးချင်း အနုပညာကုထုံး"}',
     'request', 'active', 60, 0, 15, 0),
    ('group-art-wellbeing',
     '{"en": "Group Art & Wellbeing", "my": "အုပ်စုလိုက် အနုပညာနှင့် ကိုယ်စိတ်ကျန်းမာချမ်းသာမှု"}',
     'enquiry_only', 'active', NULL, 0, 0, 1),
    ('workshops-programs',
     '{"en": "Workshops & Programs", "my": "အလုပ်ရုံဆွေးနွေးပွဲများနှင့် အစီအစဉ်များ"}',
     'enquiry_only', 'active', NULL, 0, 0, 2),
    ('free-consultation',
     '{"en": "Free consultation"}',
     'request', 'paused', 20, 0, 0, 3)
ON CONFLICT (slug) DO NOTHING;

-- +goose Down
DROP TABLE services;
DROP DOMAIN localized;
