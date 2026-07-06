// Package reports will implement the reporting and aggregation service:
// endpoints that summarize tracked activity by day, category, app, project,
// and a chronological timeline. It reads from TimescaleDB continuous
// aggregates (daily_app_totals, daily_category_totals,
// hourly_category_totals) for completed periods and combines that with a
// real-time aggregation query over the raw activity_events hypertable for
// the current, not-yet-aggregated period, so reports stay up to date without
// waiting on the aggregate refresh interval.
package reports
