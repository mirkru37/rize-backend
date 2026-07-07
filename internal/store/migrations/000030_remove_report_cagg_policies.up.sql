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
-- again, which was observed (rarely) to race TimescaleDB's background job
-- scheduler catalog cleanup ("tuple concurrently deleted") when migration
-- 000031 immediately drops the underlying view. A short pause here gives
-- that catalog cleanup time to settle before the drop; it costs nothing on
-- an already-established database where the policies are not brand new.
SELECT pg_sleep(0.5);
