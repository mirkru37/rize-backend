SELECT remove_continuous_aggregate_policy('hourly_category_totals', if_exists => true);
SELECT remove_continuous_aggregate_policy('daily_category_totals', if_exists => true);
SELECT remove_continuous_aggregate_policy('daily_app_totals', if_exists => true);
