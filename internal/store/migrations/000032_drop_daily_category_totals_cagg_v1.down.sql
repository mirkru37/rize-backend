-- Reverts migration 000032: recreate daily_category_totals in its
-- pre-RIZ-74 shape (no device_id, materialized_only left at TimescaleDB's
-- default of true), matching migration 000017.
CREATE MATERIALIZED VIEW daily_category_totals
WITH (timescaledb.continuous) AS
SELECT
    user_id,
    time_bucket('1 day', started_at) AS day,
    category_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, day, category_id;
