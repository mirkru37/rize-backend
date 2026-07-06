-- name: CreateFocusSessionForUser :one
-- POST /v1/focus-sessions per documentation/api-reference.md §CRUD groups.
-- id is caller-supplied (client-supplied-UUIDv7 entity per
-- documentation/database-schema.md's PK convention, same as
-- internal/sync's push-side UpsertFocusSession). device_id and project_id
-- are validated by the caller (internal/focussessions's service) against
-- the authenticated user before this INSERT runs, per
-- documentation/security.md §Tenant Isolation — device_id is NOT NULL on
-- this table (see documentation/database-schema.md's focus_sessions),
-- so a caller must resolve a device it owns first.
INSERT INTO focus_sessions (
    id, user_id, device_id, project_id, kind, planned_duration_s,
    started_at, ended_at, status, note, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, $5, $6,
    $7, $8, $9, $10, now(), now()
)
RETURNING *;

-- name: ListFocusSessionsForUser :many
-- Keyset-paginated list for GET /v1/focus-sessions, per
-- documentation/api-reference.md §Conventions. Scoped by user_id per
-- documentation/security.md §Tenant Isolation; excludes soft-deleted rows.
SELECT * FROM focus_sessions
WHERE user_id = $1 AND deleted_at IS NULL AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: GetFocusSessionForUser :one
-- Tenant-scoped lookup for GET /v1/focus-sessions/{id} and as the
-- authorization check before PATCH/DELETE. Zero rows for either "no such
-- session" or "belongs to another user," reported identically as 404.
SELECT * FROM focus_sessions
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: UpdateFocusSessionForUser :one
-- PATCH /v1/focus-sessions/{id}. See queries/projects.sql's
-- UpdateProjectForUser doc comment for the read-then-merge-then-write
-- pattern and the updated_at rationale (server sets updated_at = now();
-- this is a direct REST edit, not a push-path LWW write).
UPDATE focus_sessions
SET device_id = $3,
    project_id = $4,
    kind = $5,
    planned_duration_s = $6,
    started_at = $7,
    ended_at = $8,
    status = $9,
    note = $10,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteFocusSessionForUser :execrows
-- DELETE /v1/focus-sessions/{id}. Soft delete via deleted_at so the
-- deletion propagates as a tombstone through GET /v1/sync/changes.
UPDATE focus_sessions
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;
