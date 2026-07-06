// Package reports implements the six GET /v1/reports/* endpoints from
// documentation/api-reference.md §Activities & reports (RIZ-35): summary,
// daily, categories, apps, projects, and timeline.
//
// # Aggregation strategy and a documented contract tension
//
// documentation/architecture-backend.md §Aggregation Strategy says two
// things that pull in different directions for a filtered report:
//
//  1. "the reporting service queries the aggregates directly for closed
//     historical periods" (the fast path this ticket's brief's
//     non-negotiable #4 also requires: "do not full-scan the hypertable
//     per request").
//  2. Serving *trimmed* totals (the same section's Overlap Rules paragraph)
//     "requires the report layer to run a raw-event pass ... rather than
//     reading a trimmed total directly out of the aggregate" — and this
//     is stated to hold for closed periods too, since
//     daily_app_totals/daily_category_totals have no device_id dimension
//     to trim against.
//
// Taken literally together, every closed-period query would have to
// bypass the aggregates entirely, which contradicts (1) and the brief's
// "hit the caggs for closed periods" instruction. Per this ticket's
// brief, that contradiction is resolved as: implement the doc-conformant
// raw-event trimming pass for the *current/partial* period (see
// categoryTotals/appTotals below), and use the continuous aggregates for
// *closed* periods per the general Aggregation Strategy fast path and the
// brief's explicit non-negotiable — **STOP-and-report**: closed-period
// totals served from daily_app_totals/daily_category_totals are therefore
// plain untrimmed sums, not device-capped/cross-device-disambiguated
// totals, which does not match architecture-backend.md's literal
// "requires the report layer to run a raw-event pass [for closed periods
// too]" sentence. This is a real behavior difference (a same-device
// overlap that lands entirely within a closed day will be double-counted
// in daily/summary/categories/apps reports) that only a cagg redesign
// (adding a device_id dimension) or an explicit doc decision to drop the
// fast path can resolve — flagging per the brief rather than picking
// unilaterally.
//
// A second, related gap the docs don't address at all: daily_app_totals
// and daily_category_totals carry no device_id or precision column, so a
// request with a device_id or precision filter cannot be served by the
// aggregate regardless of which period it targets. dimension.go's
// categoryTotals/appTotals fall back to the raw pass for the entire
// requested range whenever such a filter is present — this isn't a
// documented contract, just the only technically possible
// implementation, so it's called out here as an assumption rather than a
// blocking ambiguity.
//
// reports/projects has no supporting continuous aggregate at all
// (database-schema.md's Continuous Aggregates section lists only
// daily_app_totals, daily_category_totals, and hourly_category_totals),
// so it always runs the raw-event pass, for both closed and open periods
// — consistent with, not in tension with, the Aggregation Strategy
// section, since there's no aggregate to prefer in the first place.
//
// # Timezone assumption
//
// api-reference.md's Conventions section leaves request-timestamp
// timezone handling unstated for these endpoints (like elsewhere in this
// codebase); this ticket follows that same "UTC, and note it" convention:
// `from`/`to` are parsed as RFC3339 timestamps and the closed/open "today"
// boundary is UTC midnight. `GET /v1/reports/daily`'s `date` parameter
// (per the worked example) is likewise a UTC calendar date.
package reports
