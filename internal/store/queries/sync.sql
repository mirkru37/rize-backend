-- name: GetAppByBundleID :one
-- Looks up the global app-catalog row for a (bundle_id, platform) pair, per
-- documentation/database-schema.md's `UNIQUE (bundle_id, platform)` on
-- `apps`. Not user-scoped: the catalog is global/cross-user by design (see
-- documentation/architecture-backend.md §Ingestion Pipeline, stage 2).
SELECT * FROM apps
WHERE bundle_id = $1 AND platform = $2;

-- name: CreateApp :one
-- Auto-creates an apps row the first time a bundle_id/platform pair is
-- observed during ingestion, per documentation/architecture-backend.md
-- §Ingestion Pipeline ("if no matching row exists, one is created
-- automatically"). ON CONFLICT DO NOTHING (rather than erroring) makes this
-- safe to call optimistically under a race with a concurrent ingestion of
-- the same new bundle_id/platform; a caller that gets zero rows back must
-- re-fetch via GetAppByBundleID.
INSERT INTO apps (
    id, bundle_id, platform, name
) VALUES (
    gen_random_uuid(), $1, $2, $3
)
ON CONFLICT (bundle_id, platform) DO NOTHING
RETURNING *;

-- name: GetUserAppSettingByUserAndApp :one
-- The user-specific override consulted by category resolution's first
-- fallback step, per documentation/architecture-backend.md §Ingestion
-- Pipeline stage 3. Scoped by user_id per documentation/security.md
-- §Tenant Isolation.
SELECT * FROM user_app_settings
WHERE user_id = $1 AND app_id = $2;

-- name: InsertActivityEvent :one
-- Idempotent insert for the append-only activity_events hypertable, per
-- documentation/sync-protocol.md §Entity Classes ("Idempotency key is the
-- composite UNIQUE (user_id, event_id, started_at) constraint"). ON
-- CONFLICT DO NOTHING means a retried/replayed push of the same event
-- returns zero rows, which the caller reports as a "duplicate" per-item
-- result rather than an error. Scoped by user_id (the row is created for
-- the authenticated caller only, never for a user_id supplied by the
-- request body) per documentation/security.md §Tenant Isolation.
INSERT INTO activity_events (
    event_id, user_id, device_id, started_at, ended_at,
    type, source, precision,
    app_id, raw_bundle_id, window_title, url,
    category_id, project_id, deleted, inserted_at
) VALUES (
    $1, $2, $3, $4, $5,
    $6, $7, $8,
    $9, $10, $11, $12,
    $13, $14, $15, now()
)
ON CONFLICT ON CONSTRAINT activity_events_idempotency_key DO NOTHING
RETURNING *;

-- name: TombstoneActivityEvent :one
-- Applies a tombstone push (documentation/sync-protocol.md: "tombstoning
-- an existing event is a subsequent push of the same event_id with
-- deleted: true and the same started_at ... no other field may change on
-- a tombstone push") against a row that already exists under its
-- idempotency key (user_id, event_id, started_at) — i.e. InsertActivityEvent
-- found a conflict and the incoming item is a delete. Only the deleted
-- column is written, preserving the append-only invariant that no other
-- column may be mutated after ingestion. WHERE deleted = false makes a
-- second tombstone push against an already-deleted row a no-op (zero rows
-- returned), which the caller reports as "duplicate" rather than
-- re-"applying" it.
UPDATE activity_events
SET deleted = true
WHERE user_id = $1 AND event_id = $2 AND started_at = $3 AND deleted = false
RETURNING *;

-- name: GetFocusSessionByID :one
-- Unscoped-by-user lookup used only to distinguish, after an
-- UpsertFocusSession call returns zero rows, whether the existing row
-- under this id belongs to a different user entirely (a tenant-isolation
-- violation, reported as "invalid") from a same-user last-write-wins loss
-- (reported as "duplicate") — see internal/sync's push service.
SELECT * FROM focus_sessions
WHERE id = $1;

-- name: UpsertFocusSession :one
-- Last-write-wins upsert for focus_sessions, per
-- documentation/sync-protocol.md §Entity Classes ("Server compares [the
-- client-supplied updated_at] against the currently stored updated_at for
-- that id ... Whichever timestamp wins, the server persists that version
-- and bumps server_seq on the row"). The WHERE clause on the DO UPDATE
-- branch is both the LWW comparison (existing.updated_at < new.updated_at)
-- and a tenant-isolation guard (existing.user_id = new.user_id): if id
-- collides with a row owned by a different user, or the incoming write is
-- not newer, zero rows are returned and the caller distinguishes the two
-- cases via GetFocusSessionByID.
INSERT INTO focus_sessions (
    id, user_id, device_id, project_id, kind, planned_duration_s,
    started_at, ended_at, status, note, created_at, updated_at, deleted_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, now(), $11, $12
)
ON CONFLICT (id) DO UPDATE SET
    device_id          = EXCLUDED.device_id,
    project_id         = EXCLUDED.project_id,
    kind               = EXCLUDED.kind,
    planned_duration_s = EXCLUDED.planned_duration_s,
    started_at         = EXCLUDED.started_at,
    ended_at           = EXCLUDED.ended_at,
    status             = EXCLUDED.status,
    note               = EXCLUDED.note,
    updated_at         = EXCLUDED.updated_at,
    deleted_at         = EXCLUDED.deleted_at
WHERE focus_sessions.user_id = EXCLUDED.user_id
  AND focus_sessions.updated_at < EXCLUDED.updated_at
RETURNING *;
