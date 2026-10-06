-- +goose Up
-- The publishing portal (ADR-009; docs/data-model.md, "Posts"): one post, a
-- version per channel, and how each channel's publishing went.
CREATE TABLE posts (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title                text NOT NULL,
    kind                 text NOT NULL,
    status               text NOT NULL DEFAULT 'draft',
    scheduled_at         timestamptz,
    consent_confirmed_at timestamptz,
    consent_confirmed_by uuid REFERENCES users (id) ON DELETE SET NULL,
    consent_note         text,
    review_note          text,
    author_id            uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_at          timestamptz,
    approved_by          uuid REFERENCES users (id) ON DELETE SET NULL,
    published_at         timestamptz,
    version              int NOT NULL DEFAULT 1,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT posts_title_length CHECK (char_length(title) BETWEEN 1 AND 200),
    CONSTRAINT posts_kind_check CHECK (kind IN ('insight', 'true_story', 'announcement')),
    CONSTRAINT posts_status_check CHECK (status IN (
        'idea', 'draft', 'in_review', 'approved', 'scheduled', 'publishing', 'published', 'archived')),
    CONSTRAINT posts_scheduled_at_required CHECK (status <> 'scheduled' OR scheduled_at IS NOT NULL),
    CONSTRAINT posts_notes_length CHECK (
        char_length(consent_note) <= 2000 AND char_length(review_note) <= 2000)
);

CREATE INDEX posts_status_created ON posts (status, created_at);

-- Image ids are not yet foreign keys: the media library arrives separately.
CREATE TABLE post_versions (
    post_id         uuid NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    channel         text NOT NULL,
    enabled         boolean NOT NULL DEFAULT false,
    slug            text,
    title           localized,
    excerpt         localized,
    body            localized,
    cover_image_id  uuid,
    seo_title       localized,
    seo_description localized,
    text            text,
    link_url        text,
    image_ids       uuid[] NOT NULL DEFAULT '{}',
    updated_at      timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (post_id, channel),
    CONSTRAINT post_versions_channel_check
        CHECK (channel IN ('website', 'facebook', 'instagram', 'linkedin')),
    CONSTRAINT post_versions_slug_format CHECK (slug ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
    -- Website fields belong to the website version, social fields to the others.
    CONSTRAINT post_versions_fields_by_channel CHECK (
        CASE channel
            WHEN 'website' THEN text IS NULL AND link_url IS NULL AND cardinality(image_ids) = 0
            ELSE num_nonnulls(slug, title, excerpt, body, cover_image_id, seo_title, seo_description) = 0
        END)
);

CREATE UNIQUE INDEX post_versions_slug_key ON post_versions (slug) WHERE channel = 'website';

CREATE TABLE post_publications (
    post_id      uuid NOT NULL REFERENCES posts (id) ON DELETE CASCADE,
    channel      text NOT NULL,
    status       text NOT NULL DEFAULT 'pending',
    external_id  text,
    permalink    text,
    error        text,
    attempts     int NOT NULL DEFAULT 0,
    published_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),

    PRIMARY KEY (post_id, channel),
    CONSTRAINT post_publications_channel_check
        CHECK (channel IN ('website', 'facebook', 'instagram', 'linkedin')),
    CONSTRAINT post_publications_status_check
        CHECK (status IN ('pending', 'publishing', 'published', 'failed', 'manual')),
    CONSTRAINT post_publications_published_at
        CHECK (status NOT IN ('published', 'manual') OR published_at IS NOT NULL)
);

CREATE INDEX post_publications_pending ON post_publications (status) WHERE status IN ('pending', 'failed');

-- +goose Down
DROP TABLE post_publications;
DROP TABLE post_versions;
DROP TABLE posts;
