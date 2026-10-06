-- +goose Up
-- The media library (docs/data-model.md, "Media"). Object keys derive from
-- the id: the original at originals/<id>.jpg, private, and each web size at
-- public/<id>/<width>.jpg, served from MEDIA_PUBLIC_URL.
CREATE TABLE media (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    width       int NOT NULL,
    height      int NOT NULL,
    widths      int[] NOT NULL,
    byte_size   bigint NOT NULL,
    alt         localized,
    credit      text,
    uploaded_by uuid REFERENCES users (id) ON DELETE SET NULL,
    version     int NOT NULL DEFAULT 1,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT media_dimensions CHECK (width > 0 AND height > 0 AND cardinality(widths) > 0),
    CONSTRAINT media_credit_length CHECK (char_length(credit) <= 300)
);

CREATE INDEX media_created ON media (created_at);

-- +goose Down
DROP TABLE media;
