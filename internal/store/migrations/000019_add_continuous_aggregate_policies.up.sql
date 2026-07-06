-- Automatic refresh policies for the three continuous aggregates, per
-- documentation/database-schema.md §Continuous Aggregates. Offsets/schedule
-- are an operational tuning choice not pinned by the schema doc: daily
-- aggregates refresh hourly over a 3-day lookback window (to fold in late
-- arrivals from offline-first clients), and the hourly aggregate refreshes
-- every 30 minutes over a 3-hour lookback window.
SELECT add_continuous_aggregate_policy('daily_app_totals',
    start_offset => INTERVAL '3 days',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');

SELECT add_continuous_aggregate_policy('daily_category_totals',
    start_offset => INTERVAL '3 days',
    end_offset => INTERVAL '1 hour',
    schedule_interval => INTERVAL '1 hour');

SELECT add_continuous_aggregate_policy('hourly_category_totals',
    start_offset => INTERVAL '3 hours',
    end_offset => INTERVAL '30 minutes',
    schedule_interval => INTERVAL '30 minutes');
