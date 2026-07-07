-- RIZ-74, step 8 of 8 — see migration 000030's comment for the full plan.
-- Offsets/schedule unchanged from migration 000019: still matter with
-- materialized_only = false, since they keep the materialized data warm
-- so a real-time query only scans a small, recent range of raw rows
-- instead of the whole history on every request.
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
