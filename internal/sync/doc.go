// Package sync implements both halves of the sync protocol:
// batched, idempotent event upload from the desktop and mobile clients
// (POST /v1/sync/events, RIZ-33) and the cursor-paginated pull of changes
// back to a client (GET /v1/sync/changes, RIZ-34), per
// documentation/sync-protocol.md and documentation/architecture-backend.md
// §Ingestion Pipeline.
//
// RIZ-33 scope: POST /v1/sync/events only (batch validation, app-catalog
// resolution, category resolution, idempotent insert into activity_events,
// last-write-wins upsert into focus_sessions).
//
// Entity types other than "activity_event" and "focus_session" — "project",
// "tag", "user_app_setting" — are valid discriminators per
// documentation/sync-protocol.md §Push but their storage paths are not yet
// implemented; items of those types are reported as "invalid" with error
// code UNSUPPORTED_ENTITY_TYPE (see service.go's assumption note). This
// mirrors requirement 5 of the RIZ-33 brief, which scopes the sqlc query
// work for this ticket to exactly the activity_events insert path and the
// focus_sessions LWW upsert path.
//
// RIZ-34 scope: GET /v1/sync/changes, delivering upserts/tombstones for
// activity_events, focus_sessions, projects, tags, and user_app_settings.
// "categories" and "aggregates" are intentionally NOT included in the pull
// response — see pull.go's package-level doc comment for the documented
// contract ambiguity that motivates leaving them out rather than guessing
// a shape for them.
package sync
