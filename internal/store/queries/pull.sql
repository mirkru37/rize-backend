-- name: ListActivityEventChangesForUser :many
-- One page of GET /v1/sync/changes's "activity_events" entity, per
-- documentation/sync-protocol.md §Pull. Scoped by user_id per
-- documentation/security.md §Tenant Isolation.
--
-- Pagination/ordering key (RIZ-34 H1 re-review fix, migration 000025): the
-- tuple (xmin_xid8(xmin), server_seq), NOT server_seq alone. server_seq
-- (migration 000021/000022's nextval()-assigned counter) and xid
-- assignment are independent, so a transaction can be handed a LOWER
-- server_seq than one that later commits with a HIGHER xid — a
-- server_seq-only cursor can then advance past a row that is still
-- in-flight and permanently skip it once it finally commits. Anchoring the
-- keyset to the SAME xid8 the horizon gate below already uses makes the
-- settled (xid8 < horizon) prefix append-only under (xid8, server_seq)
-- order, so keyset pagination over it is gap-free by construction
-- regardless of server_seq's assignment order. See migration 000025's
-- comment for the full invariant.
--
-- xmin_xid8(ae.xmin) < pg_snapshot_xmin(pg_current_snapshot()) is the
-- horizon gate itself (equivalent to xid_before_snapshot_horizon, inlined
-- here so this file's ORDER BY/predicate both reference the same
-- xmin_xid8 value): only rows whose inserting/updating transaction is
-- fully, permanently committed as of this pull's MVCC snapshot are
-- delivered. cursor_xid8/cursor_seq are the caller's opaque cursor's decoded
-- (xid8, server_seq) tuple (the zero cursor, (0, 0), means "from the
-- beginning" since real xids/server_seqs are always > 0); page_limit is the
-- caller-requested page size PLUS ONE (see internal/sync's pull service),
-- so the caller can detect "more rows exist beyond this page" without a
-- second round trip. app_bundle_id and category_name are resolved via LEFT
-- JOIN so a null app_id/category_id (never yet resolved) doesn't drop the
-- row, matching documentation/sync-protocol.md's upsert shape
-- ({"app_bundle_id": ..., "category": ...}).
--
-- This query MUST run inside the same REPEATABLE READ transaction as every
-- other pull query in the same request (internal/sync's pull service opens
-- it), so pg_current_snapshot() resolves to one stable snapshot across all
-- of them.
SELECT
    ae.event_id,
    ae.started_at,
    ae.ended_at,
    a.bundle_id AS app_bundle_id,
    c.name AS category_name,
    ae.precision,
    ae.deleted,
    ae.server_seq,
    xmin_xid8(ae.xmin) AS xid8
FROM activity_events ae
LEFT JOIN apps a ON a.id = ae.app_id
LEFT JOIN categories c ON c.id = ae.category_id
WHERE ae.user_id = $1
    AND xmin_xid8(ae.xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(ae.xmin), ae.server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(ae.xmin) ASC, ae.server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: ListFocusSessionChangesForUser :many
-- One page of GET /v1/sync/changes's "focus_sessions" entity. See
-- ListActivityEventChangesForUser's doc comment for the pagination,
-- tenant-scoping, and xmin-horizon rationale, which applies identically
-- here.
SELECT id, project_id, kind, planned_duration_s, started_at, ended_at,
       status, note, updated_at, deleted_at, server_seq,
       xmin_xid8(xmin) AS xid8
FROM focus_sessions
WHERE user_id = $1
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: ListProjectChangesForUser :many
-- One page of GET /v1/sync/changes's "projects" entity. See
-- ListActivityEventChangesForUser's doc comment for the pagination and
-- xmin-horizon rationale.
SELECT id, name, color, archived_at, updated_at, deleted_at, server_seq,
       xmin_xid8(xmin) AS xid8
FROM projects
WHERE user_id = $1
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: ListTagChangesForUser :many
-- One page of GET /v1/sync/changes's "tags" entity. See
-- ListActivityEventChangesForUser's doc comment for the pagination and
-- xmin-horizon rationale.
SELECT id, name, updated_at, deleted_at, server_seq,
       xmin_xid8(xmin) AS xid8
FROM tags
WHERE user_id = $1
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: ListUserAppSettingChangesForUser :many
-- One page of GET /v1/sync/changes's "user_app_settings" entity.
-- user_app_settings has no deleted_at/deleted column (see
-- documentation/database-schema.md), so every row from this query is an
-- upsert; internal/sync's pull service always reports an empty
-- "tombstones" array for this entity type. See
-- ListActivityEventChangesForUser's doc comment for the pagination and
-- xmin-horizon rationale.
SELECT user_id, app_id, category_id, excluded, updated_at, server_seq,
       xmin_xid8(xmin) AS xid8
FROM user_app_settings
WHERE user_id = $1
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);

-- name: ListCategoryChangesForUser :many
-- One page of GET /v1/sync/changes's "categories" entity (RIZ-34 M1).
-- database-schema.md states server_seq-based keyset pagination applies to
-- categories exactly like every other syncable table (now over the
-- (xid8, server_seq) tuple, see ListActivityEventChangesForUser's doc
-- comment); scoping mirrors internal/store/queries/categories.sql's
-- ListCategoriesForUser: a user's pull sees the union of every system
-- default category (user_id IS NULL) plus their own custom categories
-- (user_id = $1), so a client can resolve every category_id it might see
-- on an activity_event/user_app_setting row. See
-- ListActivityEventChangesForUser's doc comment for the pagination and
-- xmin-horizon rationale.
SELECT id, user_id, name, color, productivity, updated_at, deleted_at, server_seq,
       xmin_xid8(xmin) AS xid8
FROM categories
WHERE (user_id = $1 OR user_id IS NULL)
    AND xmin_xid8(xmin) < pg_snapshot_xmin(pg_current_snapshot())
    AND (xmin_xid8(xmin), server_seq) > (sqlc.arg(cursor_xid8)::xid8, sqlc.arg(cursor_seq)::bigint)
ORDER BY xmin_xid8(xmin) ASC, server_seq ASC
LIMIT sqlc.arg(page_limit);
