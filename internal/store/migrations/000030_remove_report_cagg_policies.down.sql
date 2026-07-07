-- Reverts migration 000030: re-add the pre-RIZ-74 refresh policies, same
-- offsets/schedule as migration 000019.
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
