CREATE MATERIALIZED VIEW daily_app_totals
WITH (timescaledb.continuous) AS
SELECT
    user_id,
    time_bucket('1 day', started_at) AS day,
    app_id,
    sum(duration_s) AS total_s
FROM activity_events
GROUP BY user_id, day, app_id;
