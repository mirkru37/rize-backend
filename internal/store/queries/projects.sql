-- name: CreateProjectForUser :one
-- Creates a project owned by the authenticated user, per
-- documentation/api-reference.md §CRUD groups ("POST /v1/projects"). id is
-- supplied by the caller (see internal/projects's uuid handling): per
-- documentation/database-schema.md's PK convention, projects are a
-- client-supplied-UUIDv7 entity like tags/focus_sessions, whether the row
-- originates from a sync push or a direct REST create — this query does
-- not itself generate one.
INSERT INTO projects (
    id, user_id, name, color, created_at, updated_at
) VALUES (
    $1, $2, $3, $4, now(), now()
)
RETURNING *;

-- name: ListProjectsForUser :many
-- Keyset-paginated list for GET /v1/projects, per
-- documentation/api-reference.md §Conventions ("list endpoints ... use a
-- cursor-based convention"). Soft-deleted rows are excluded: a CRUD list is
-- "your current projects," not the sync tombstone stream (GET
-- /v1/sync/changes serves that). Scoped by user_id per
-- documentation/security.md §Tenant Isolation.
SELECT * FROM projects
WHERE user_id = $1 AND deleted_at IS NULL AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: GetProjectForUser :one
-- Tenant-scoped lookup for GET /v1/projects/{id}, and the authorization
-- check before PATCH/DELETE. Unlike sync.sql's GetProjectByIDForUser (used
-- by the push path to validate a focus_session's project_id reference,
-- where a soft-deleted project should arguably still resolve — that
-- existing behavior is out of scope for this ticket and left unchanged),
-- this query excludes soft-deleted rows: a deleted project must read back
-- as 404, matching documentation/database-schema.md's soft-delete
-- convention and this ticket's cross-tenant/deleted 404-equivalence
-- convention.
SELECT * FROM projects
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;

-- name: UpdateProjectForUser :one
-- Partial update for PATCH /v1/projects/{id}. The service layer reads the
-- current row first and merges caller-supplied fields onto it (matching
-- internal/auth.UpdateProfile's pattern), so every column here is always
-- the final, already-merged value rather than a SQL-side COALESCE.
-- updated_at is always set to the server's now() — direct REST edits are
-- not subject to the client-supplied-updated_at LWW comparison
-- documentation/sync-protocol.md defines for the push path; the
-- server_seq bump (migration 000022's trigger) is what a later sync pull
-- propagates to other devices, and any device that later pushes an older
-- focus_session/project/tag write than this REST edit's new updated_at
-- correctly loses the LWW comparison.
UPDATE projects
SET name = $3,
    color = $4,
    archived_at = $5,
    updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteProjectForUser :execrows
-- DELETE /v1/projects/{id}. Soft delete via deleted_at per
-- documentation/database-schema.md's "Soft delete via deleted_at"
-- convention, so the deletion propagates to other devices as a tombstone
-- through GET /v1/sync/changes. Scoped by user_id per
-- documentation/security.md §Tenant Isolation; the returned row count lets
-- the caller distinguish "already deleted / not yours / doesn't exist"
-- (0 rows) from a successful delete (1 row), all reported identically as
-- 404 to avoid leaking cross-tenant existence.
UPDATE projects
SET deleted_at = now(), updated_at = now()
WHERE id = $1 AND user_id = $2 AND deleted_at IS NULL;
