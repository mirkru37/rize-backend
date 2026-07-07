-- RIZ-74, step 5 of 8 — see migration 000030's comment for the full plan.
--
-- device_id closes the "closed-period trim gap" documented in
-- documentation/architecture-backend.md §Aggregation Strategy: as
-- originally defined (migration 000016), daily_app_totals summed
-- duration_s per time_bucket with no device dimension at all, so it could
-- not reproduce the report layer's raw-event same-device overlap cap
-- (documentation/sync-protocol.md §Overlap Rules; internal/reports/trim.go
-- implements it for raw events) — closed-period totals read from the
-- aggregate could double-count same-device overlapping activity_events
-- rows that the raw/open-period path already caps. With device_id in the
-- GROUP BY, the report-query layer (internal/store/queries/activities.sql's
-- AppTotalsForRange) can sum each device's total_s across the requested
-- window and cap it at the window's own length before summing across
-- devices — the same per-device "never contribute more active time to a
-- window than the window contains" invariant the raw path applies, giving
-- closed-period results equivalent to the raw/open-period path. This is
-- exactly the "future, separately documented continuous-aggregate redesign
-- (adding a device dimension and window-capping logic)" the architecture
-- doc anticipated.
--
-- materialized_only = false makes this a real-time aggregate: querying
-- the view transparently unions its materialized data with a live query
-- over the still-unmaterialized tail of activity_events, so a just-closed
-- day's totals are correct immediately rather than only after the next
-- add_continuous_aggregate_policy refresh run (migration 000037 re-adds
-- that policy; it still matters with materialized_only = false, since it
-- keeps the materialized data warm so a real-time query only has to scan a
-- small, recent range of raw rows instead of the whole history on every
-- request — real-time aggregation covers the freshness lag between
-- refreshes, the policy covers query performance).
--
-- This cagg carries no production data yet (RIZ-35 shipped it; nothing
-- else depends on its current definition), so dropping (migration 000031)
-- and recreating it here is acceptable rather than an in-place ALTER
-- (which TimescaleDB does not support for a continuous aggregate's
-- grouping columns) per the RIZ-74 brief. No indexes existed on this cagg
-- before, so none need to be recreated here.
CREATE MATERIALIZED VIEW daily_app_totals
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    user_id,
    time_bucket('1 day', started_at) AS day,
    app_id,
    device_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, day, app_id, device_id;
