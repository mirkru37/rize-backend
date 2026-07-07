-- RIZ-74, step 5 of 8 — see migration 000030's comment for the full plan.
--
-- device_id narrows (does not close) the "closed-period trim gap"
-- documented in documentation/architecture-backend.md §Aggregation
-- Strategy: as originally defined (migration 000016), daily_app_totals
-- summed duration_s per time_bucket with no device dimension at all, so a
-- same-device overlap could inflate a closed-period total with no bound
-- whatsoever. With device_id in the GROUP BY, the report-query layer
-- (internal/store/queries/activities.sql's AppTotalsForRange) can sum
-- each device's total_s across the requested window and cap it at the
-- window's own length before summing across devices — an UPPER BOUND on
-- same-device overlap inflation, not a reproduction of the raw/open-period
-- path's interval merge (documentation/sync-protocol.md §Overlap Rules;
-- internal/reports/trim.go implements the actual merge for raw events).
-- The cap and the raw merge agree only when a device's merged coverage
-- spans the whole window; otherwise the cap can still overstate the
-- total relative to the raw path. Concretely, for a 24h window with two
-- same-device events 00:00-06:00 and 03:00-09:00 (naive sum 12h, merged
-- coverage 9h): the raw path returns 9h, while this cap returns
-- min(12h, 24h) = 12h — no double-counting relative to the naive sum, but
-- still 3h higher than the raw path's true merged total. This is a
-- deliberately partial version of the "future, separately documented
-- continuous-aggregate redesign (adding a device dimension and
-- window-capping logic)" the architecture doc anticipated: it removes the
-- previously-unbounded inflation, not all of it.
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
