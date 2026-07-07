-- RIZ-74, step 6 of 8 — see migrations 000030 and 000034's comments for
-- the full plan and rationale (device_id closes the closed-period trim
-- gap; materialized_only = false makes just-closed days real-time).
CREATE MATERIALIZED VIEW daily_category_totals
WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
SELECT
    user_id,
    time_bucket('1 day', started_at) AS day,
    category_id,
    device_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, day, category_id, device_id;
