DROP TABLE IF EXISTS sync_changelog_horizon;
DROP INDEX IF EXISTS sync_changelog_created_at_idx;
ALTER TABLE sync_changelog DROP COLUMN IF EXISTS created_at;
