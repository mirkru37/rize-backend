CREATE MATERIALIZED VIEW hourly_category_totals
WITH (timescaledb.continuous) AS
SELECT
    user_id,
    time_bucket('1 hour', started_at) AS hour,
    category_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, hour, category_id;
