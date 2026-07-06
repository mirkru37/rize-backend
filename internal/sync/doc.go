// Package sync will implement the ingestion service for activity events
// uploaded by the desktop and mobile clients: batch validation, app-catalog
// resolution (looking up or creating an apps row for an event's bundle ID),
// category resolution (a user-specific override in user_app_settings
// falling back to the app's default category), and idempotent upsert of
// events into the activity_events hypertable keyed by their client-generated
// UUIDv7 id. It also serves the cursor-based pull side of sync, returning
// upserts and tombstones since a given cursor. Full semantics are defined in
// documentation/sync-protocol.md.
package sync
