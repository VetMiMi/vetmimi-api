-- +goose Up
-- What visitors read of each post's website version (docs/data-model.md,
-- "Posts"): a copy taken when the post is published, so an edit going back
-- through review leaves the live article as it was until it is published
-- again.
CREATE TABLE published_articles (
    post_id         uuid PRIMARY KEY REFERENCES posts (id) ON DELETE CASCADE,
    slug            text NOT NULL,
    title           localized NOT NULL,
    excerpt         localized,
    body            localized NOT NULL,
    cover_image_id  uuid,
    seo_title       localized,
    seo_description localized,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX published_articles_slug_key ON published_articles (slug);

INSERT INTO published_articles (post_id, slug, title, excerpt, body, cover_image_id, seo_title,
                                seo_description, updated_at)
SELECT v.post_id, v.slug, v.title, v.excerpt, v.body, v.cover_image_id, v.seo_title, v.seo_description,
       v.updated_at
FROM post_versions v
JOIN posts p ON p.id = v.post_id
JOIN post_publications pub ON pub.post_id = v.post_id AND pub.channel = 'website'
WHERE v.channel = 'website' AND v.enabled AND pub.status = 'published'
  AND p.status IN ('publishing', 'published');

-- +goose Down
DROP TABLE published_articles;
