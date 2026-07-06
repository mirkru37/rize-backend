-- name: ListActivityEventChangesForUser :many
-- One page of GET /v1/sync/changes's "activity_events" entity, per
-- documentation/sync-protocol.md §Pull. Scoped by user_id per
-- documentation/security.md §Tenant Isolation; ordered by the global
-- server_seq sequence (migration 000021/000022) for strict keyset
-- pagination. $3 is the caller-requested page size PLUS ONE (see
-- internal/sync's pull service), so the caller can detect "more rows
-- exist beyond this page" without a second round trip. app_bundle_id and
-- category_name are resolved via LEFT JOIN so a null app_id/category_id
-- (never yet resolved) doesn't drop the row, matching
-- documentation/sync-protocol.md's upsert shape
-- ({"app_bundle_id": ..., "category": ...}).
SELECT
    ae.event_id,
    ae.started_at,
    ae.ended_at,
    a.bundle_id AS app_bundle_id,
    c.name AS category_name,
    ae.precision,
    ae.deleted,
    ae.server_seq
FROM activity_events ae
LEFT JOIN apps a ON a.id = ae.app_id
LEFT JOIN categories c ON c.id = ae.category_id
WHERE ae.user_id = $1 AND ae.server_seq > $2
ORDER BY ae.server_seq ASC
LIMIT $3;

-- name: ListFocusSessionChangesForUser :many
-- One page of GET /v1/sync/changes's "focus_sessions" entity. See
-- ListActivityEventChangesForUser's doc comment for the pagination and
-- tenant-scoping rationale, which applies identically here.
SELECT id, project_id, kind, planned_duration_s, started_at, ended_at,
       status, note, updated_at, deleted_at, server_seq
FROM focus_sessions
WHERE user_id = $1 AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: ListProjectChangesForUser :many
-- One page of GET /v1/sync/changes's "projects" entity.
SELECT id, name, color, archived_at, updated_at, deleted_at, server_seq
FROM projects
WHERE user_id = $1 AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: ListTagChangesForUser :many
-- One page of GET /v1/sync/changes's "tags" entity.
SELECT id, name, updated_at, deleted_at, server_seq
FROM tags
WHERE user_id = $1 AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;

-- name: ListUserAppSettingChangesForUser :many
-- One page of GET /v1/sync/changes's "user_app_settings" entity.
-- user_app_settings has no deleted_at/deleted column (see
-- documentation/database-schema.md), so every row from this query is an
-- upsert; internal/sync's pull service always reports an empty
-- "tombstones" array for this entity type.
SELECT user_id, app_id, category_id, excluded, updated_at, server_seq
FROM user_app_settings
WHERE user_id = $1 AND server_seq > $2
ORDER BY server_seq ASC
LIMIT $3;
