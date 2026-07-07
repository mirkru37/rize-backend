-- RIZ-72: sync_changelog retention. Migration 000026's own comment already
-- flagged that this table's unbounded growth is deferred to a future
-- ticket, and that migration 000024/000025's xid8-widening trick for the
-- commit-ordered pull cursor is only valid while every LIVE
-- sync_changelog row's raw (32-bit) xmin stays within 2^31 transactions of
-- the current xid -- a bound Postgres's own freezing/vacuuming does NOT
-- provide on its own (freezing preserves visibility across wraparound, not
-- the raw xmin value used here as an ordering key). This migration adds
-- what retention needs:
--
--   1. created_at: sync_changelog carried no timestamp column before this
--      migration, so there was nothing to prune by age. Added NOT NULL
--      DEFAULT now() -- a constant default, which PG11+ applies as a
--      metadata-only change (no table rewrite) -- so every pre-existing
--      row is backfilled with this migration's execution time. That's a
--      deliberate approximation (this migration cannot know a
--      pre-existing row's actual original write time), but it is safe:
--      it only pushes those rows' effective retention window LATER
--      (measured from migration-apply time, not from whenever they were
--      actually written), which is the conservative direction -- it can
--      never cause a row to be pruned earlier than the real policy
--      intends.
--   2. sync_changelog_horizon: a single persisted row recording the
--      furthest (xid8, server_seq) position ever pruned through, updated
--      transactionally with each prune batch (internal/sync's retention
--      pruner). This is what lets GET /v1/sync/changes (internal/sync/
--      pull.go) detect "this client's cursor is now behind pruned data"
--      and return the cursor-reset signal documentation/sync-protocol.md
--      already describes for a stale/lost cursor, instead of silently
--      serving a gappy page.
--
-- The "id boolean PRIMARY KEY DEFAULT true CHECK (id)" shape below is the
-- standard Postgres singleton-table trick: id can only ever be `true` (the
-- CHECK forces it), and a PRIMARY KEY forbids a second row with the same
-- value, so this table can never hold more than the one seeded row. No
-- device-cursor tracking is introduced here -- sync_cursors remains
-- untouched and out of scope (tracked separately as RIZ-78); this
-- migration's horizon is a single global watermark, not per-device state.
ALTER TABLE sync_changelog ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

-- Supports the retention pruner's "oldest rows first, bounded batch" scan
-- (internal/store/queries/sync_retention.sql's SelectChangelogPruneBatch),
-- which filters on created_at and orders by changelog_id for a stable,
-- resumable scan order across successive batches.
CREATE INDEX sync_changelog_created_at_idx ON sync_changelog (created_at);

CREATE TABLE sync_changelog_horizon (
    id                 boolean PRIMARY KEY DEFAULT true CHECK (id),
    horizon_xid8       xid8 NOT NULL DEFAULT '0'::xid8,
    horizon_server_seq bigint NOT NULL DEFAULT 0,
    pruned_at          timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);

COMMENT ON TABLE sync_changelog_horizon IS
    'RIZ-72: single-row watermark recording the furthest (xid8, server_seq) position pruned from sync_changelog so far. A pull cursor strictly below this position can no longer be served a gap-free page (the rows between it and here were deleted by age-based retention) and must be told to reset instead of silently skipping ahead.';

-- Seed the one permitted row. horizon_xid8/horizon_server_seq default to
-- the zero PullCursor value (see internal/store/cursor.go's PullCursor
-- doc comment: "(0, 0) sorts before every real row's tuple"), meaning
-- "nothing has been pruned yet" -- every existing cursor, including the
-- empty/zero one, is at or above the horizon until the first prune batch
-- actually deletes something.
INSERT INTO sync_changelog_horizon (id) VALUES (true);
