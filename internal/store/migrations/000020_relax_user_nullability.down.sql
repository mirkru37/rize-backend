-- Restoring NOT NULL requires backfilling any rows that were inserted
-- without a value while the constraint was relaxed.
UPDATE users SET display_name = '' WHERE display_name IS NULL;
UPDATE users SET timezone = 'UTC' WHERE timezone IS NULL;

ALTER TABLE users
    ALTER COLUMN display_name SET NOT NULL,
    ALTER COLUMN timezone SET NOT NULL;
