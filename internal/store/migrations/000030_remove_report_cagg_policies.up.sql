-- RIZ-74 (PR #9 review follow-up), step 1 of 8 (migrations 000030-000037):
-- recreates daily_app_totals, daily_category_totals, and
-- hourly_category_totals with a device_id dimension and
-- timescaledb.materialized_only = false. See migration 000034's comment
-- for the full rationale.
--
-- TimescaleDB refuses to run `CREATE MATERIALIZED VIEW ... WITH
-- (timescaledb.continuous)` as part of a multi-statement exec ("cannot run
-- inside a transaction block"), and golang-migrate sends each migration
-- file to the driver as a single multi-statement exec — the same reason
-- migrations 000016-000018 each contain exactly one such CREATE statement.
-- Recreating three continuous aggregates therefore needs to be spread
-- across several migration files, one CREATE (or, here, one policy-only)
-- statement group per file:
--   000030 (this file): remove the old policies (must happen before the
--     views they reference are dropped).
--   000031-000033: drop the old (device-unaware) daily_app_totals,
--     daily_category_totals, and hourly_category_totals views, one per
--     file.
--   000034-000036: create the new device-aware, real-time versions of the
--     same three views, one per file.
--   000037: add the new policies back.
SELECT remove_continuous_aggregate_policy('hourly_category_totals', if_exists => true);
SELECT remove_continuous_aggregate_policy('daily_category_totals', if_exists => true);
SELECT remove_continuous_aggregate_policy('daily_app_totals', if_exists => true);

-- Defensive: on a brand-new database, migrations 000016-000019 created
-- these policies mere milliseconds before this migration removes them
-- again, which was observed (rarely) to race a "TimescaleDB Background
-- Worker" backend still executing a scheduled job against these caggs
-- ("tuple concurrently deleted") when migration 000031 immediately drops
-- the underlying view. remove_continuous_aggregate_policy above removes
-- the job's catalog row synchronously, but does not wait for an
-- already-launched worker PROCESS to exit, so this polls
-- pg_stat_activity (bounded: up to 20 * 50ms = 1s) for that backend_type
-- to disappear before proceeding, rather than blindly sleeping a fixed
-- duration on every run — the common case (no worker was ever launched
-- yet) exits the loop immediately.
DO $$
DECLARE
    remaining int := 20;
BEGIN
    WHILE remaining > 0 AND EXISTS (
        SELECT 1 FROM pg_stat_activity WHERE backend_type = 'TimescaleDB Background Worker'
    ) LOOP
        PERFORM pg_sleep(0.05);
        remaining := remaining - 1;
    END LOOP;
END $$;
