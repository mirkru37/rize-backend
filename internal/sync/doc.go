// Package sync implements the ingestion (push) half of the sync protocol:
// batched, idempotent event upload from the desktop and mobile clients, per
// documentation/sync-protocol.md §Push and documentation/architecture-backend.md
// §Ingestion Pipeline.
//
// RIZ-33 scope: POST /v1/sync/events only (batch validation, app-catalog
// resolution, category resolution, idempotent insert into activity_events,
// last-write-wins upsert into focus_sessions). The pull side (GET
// /v1/sync/changes) is out of scope for this ticket and is not implemented
// here.
//
// Entity types other than "activity_event" and "focus_session" — "project",
// "tag", "user_app_setting" — are valid discriminators per
// documentation/sync-protocol.md §Push but their storage paths are not yet
// implemented; items of those types are reported as "invalid" with error
// code UNSUPPORTED_ENTITY_TYPE (see service.go's assumption note). This
// mirrors requirement 5 of the RIZ-33 brief, which scopes the sqlc query
// work for this ticket to exactly the activity_events insert path and the
// focus_sessions LWW upsert path.
package sync
