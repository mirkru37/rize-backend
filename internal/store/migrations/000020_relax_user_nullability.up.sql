-- documentation/database-schema.md lists users.display_name and
-- users.timezone without a NOT NULL constraint: an Apple Sign In user may
-- authenticate without ever supplying a display name or timezone, so the
-- schema must allow both to be absent at creation time.
ALTER TABLE users
    ALTER COLUMN display_name DROP NOT NULL,
    ALTER COLUMN timezone DROP NOT NULL;
