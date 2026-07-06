// Package activities implements GET /v1/activities per
// documentation/api-reference.md §Activities & reports (RIZ-35): raw
// tracked events for the authenticated user, filterable by time range,
// app, category, project, device, and precision, and keyset-paginated in
// chronological order.
//
// This endpoint returns raw activity_events rows unmodified — no
// overlap trimming is applied here. Trimming only applies to the derived
// aggregates served by internal/reports, per
// documentation/architecture-backend.md §Aggregation Strategy and
// documentation/sync-protocol.md §Overlap Rules ("same-device overlapping
// intervals are ingested unmodified ... trimming happens at
// report/aggregation time, not at ingestion").
package activities
