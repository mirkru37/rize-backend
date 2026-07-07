-- RIZ-74, step 7 of 8 — see migrations 000030 and 000034's comments for
-- the full plan and rationale (device_id narrows the closed-period trim
-- gap to an upper bound, not a reproduction of the raw path's interval
-- merge; materialized_only = false makes just-closed periods real-time).
--
-- No report query reads this cagg by device_id today (internal/reports
-- has no hourly-bucketed report endpoint); it carries device_id purely
-- for consistency with daily_app_totals/daily_category_totals. If a
-- future query sums total_s here without also grouping by device_id and
-- capping per device the way CategoryTotalsForRange/AppTotalsForRange do,
-- it will double-count same-device overlap exactly like the pre-RIZ-74
-- daily caggs did.
CREATE MATERIALIZED VIEW hourly_category_totals
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    user_id,
    time_bucket('1 hour', started_at) AS hour,
    category_id,
    device_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, hour, category_id, device_id;
