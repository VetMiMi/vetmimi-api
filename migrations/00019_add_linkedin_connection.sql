-- +goose Up
-- LinkedIn joins Meta in connections (docs/data-model.md, "Connections"): the
-- row holds Daw Mi's member id and name and her sealed access token, which
-- LinkedIn expires after about 60 days.
ALTER TABLE connections DROP CONSTRAINT connections_platform_check;
ALTER TABLE connections ADD CONSTRAINT connections_platform_check CHECK (platform IN ('meta', 'linkedin'));

-- +goose Down
DELETE FROM connections WHERE platform = 'linkedin';
ALTER TABLE connections DROP CONSTRAINT connections_platform_check;
ALTER TABLE connections ADD CONSTRAINT connections_platform_check CHECK (platform IN ('meta'));
