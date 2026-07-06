-- name: ListChangelogPage :many
-- One page of GET /v1/sync/changes's raw change feed, per
-- documentation/sync-protocol.md §Pull. RIZ-34 (pivot): this single query
-- replaces the six separate per-entity-table pagination queries this file
-- used to define (ListActivityEventChangesForUser and friends), which
-- paginated each syncable table directly by its own
-- (xmin_xid8(xmin), server_seq) tuple -- fatal for activity_events, a
-- compressed hypertable that rejects any xmin reference on a compressed
-- chunk. See migration 000026's header comment for the full rationale.
--
-- Pagination/ordering key is still the tuple
-- (xmin_xid8(sync_changelog.xmin), server_seq) migration 000025
-- established -- unchanged in shape, just anchored to sync_changelog's own
-- xmin (a plain, never-compressed heap table) instead of each entity
-- table's. xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot()) is the
-- same horizon gate as before: only changelog rows whose inserting
-- transaction is fully, permanently committed as of this pull's MVCC
-- snapshot are delivered, so a page can never include a row whose
-- transaction might still be in flight (see migration 000024's comment for
-- the full invariant). cursor_xid8/cursor_seq are the caller's opaque
-- cursor's decoded (xid8, server_seq) tuple; page_limit is the
-- caller-requested page size PLUS ONE (see internal/sync/pull.go), so the
-- caller can detect "more rows exist beyond this page" without a second
-- round trip.
--
-- `(user_id = $1 OR user_id IS NULL)` mirrors ListCategoryChangesForUser's
-- old scoping (a category's changelog row has user_id NULL exactly when
-- the category itself is a system default): every other entity type's
-- changelog rows always have user_id set to the actual owner, so this
-- OR only ever matches system-category rows in practice, never leaking a
-- non-category row across tenants.
--
-- This query MUST run inside the same REPEATABLE READ transaction as the
-- rest of a single pull request (internal/sync/pull.go opens it), so
-- pg_current_snapshot() resolves to one stable snapshot for the horizon
-- gate.
SELECT
    changelog_id,
    user_id,
    entity_type,
    entity_id,
    event_started_at,
    server_seq,
    xmin_xid8(xmin) AS xid8
FROM sync_changelog
WHERE (user_id = $1 OR user_id IS NULL)
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: GetActivityEventForChangelogEntry :one
-- Resolves a sync_changelog 'activity_events' entry to the entity's current
-- state, per internal/sync/pull.go's per-page dedupe-then-resolve step.
-- Binds started_at (the hypertable's partitioning column, carried on the
-- changelog row precisely so this lookup can supply it) in addition to
-- user_id/event_id: ordinary (non-system-column) reads against a
-- compressed chunk are fully supported by TimescaleDB -- only a bare
-- system-column reference (this query has none) is restricted -- so this
-- lookup succeeds unconditionally, including against a compressed chunk,
-- which is the entire point of this migration's pivot away from querying
-- activity_events.xmin directly. Zero rows is possible (see pull.go's doc
-- comment on why that's safe to skip) but not expected in steady state:
-- activity_events rows are never hard-deleted.
SELECT
    ae.event_id,
    ae.user_id,
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
WHERE ae.user_id = $1 AND ae.event_id = $2 AND ae.started_at = $3;

-- name: GetFocusSessionForChangelogEntry :one
-- Resolves a sync_changelog 'focus_sessions' entry to the entity's current
-- state. See GetActivityEventForChangelogEntry's doc comment for the
-- resolve-step rationale.
SELECT id, project_id, kind, planned_duration_s, started_at, ended_at,
       status, note, updated_at, deleted_at, server_seq
FROM focus_sessions
WHERE user_id = $1 AND id = $2;

-- name: GetProjectForChangelogEntry :one
-- Resolves a sync_changelog 'projects' entry to the entity's current state.
-- See GetActivityEventForChangelogEntry's doc comment for the
-- resolve-step rationale.
SELECT id, name, color, archived_at, updated_at, deleted_at, server_seq
FROM projects
WHERE user_id = $1 AND id = $2;

-- name: GetTagForChangelogEntry :one
-- Resolves a sync_changelog 'tags' entry to the entity's current state. See
-- GetActivityEventForChangelogEntry's doc comment for the resolve-step
-- rationale.
SELECT id, name, updated_at, deleted_at, server_seq
FROM tags
WHERE user_id = $1 AND id = $2;

-- name: GetUserAppSettingForChangelogEntry :one
-- Resolves a sync_changelog 'user_app_settings' entry to the entity's
-- current state. entity_id on the changelog row is app_id (this table's
-- primary key is the composite (user_id, app_id); user_id is already the
-- changelog row's own user_id column, per migration 000026's comment). No
-- tombstone path: user_app_settings has no deleted_at/deleted column (see
-- documentation/database-schema.md), so every resolved row is an upsert.
SELECT user_id, app_id, category_id, excluded, updated_at, server_seq
FROM user_app_settings
WHERE user_id = $1 AND app_id = $2;

-- name: GetCategoryForChangelogEntry :one
-- Resolves a sync_changelog 'categories' entry to the entity's current
-- state. `(user_id = $2 OR user_id IS NULL)` mirrors the old
-- ListCategoryChangesForUser's scoping: a category row's own user_id
-- always matches the changelog row's user_id that produced the entry
-- (both NULL for a system default, both set to the same owner otherwise),
-- so this is a tenant-safe point lookup, not a broadening of scope.
SELECT id, user_id, name, color, productivity, updated_at, deleted_at, server_seq
FROM categories
WHERE id = $1 AND (user_id = $2 OR user_id IS NULL);
