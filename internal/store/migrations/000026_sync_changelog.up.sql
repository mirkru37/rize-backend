-- RIZ-34 (pivot, live-reproduced fatal bug): migrations 000024/000025 built
-- GET /v1/sync/changes's commit-ordered pull cursor directly on top of each
-- syncable table's own `xmin` system column via
-- `(xmin_xid8(xmin), server_seq)` keyset pagination. That breaks
-- permanently for activity_events: it is a compressed hypertable (7-day
-- chunks, 30-day compression policy, migration 000011), and TimescaleDB
-- rejects ANY system-column reference other than tableoid against a
-- compressed chunk --
--   ERROR: transparent decompression only supports tableoid system column
-- -- so the very first time an activity_events chunk crosses the 30-day
-- compression threshold, ListActivityEventChangesForUser's
-- `xmin_xid8(ae.xmin)` reference starts failing with a 500 on every pull
-- that would touch that chunk, permanently (compression is not undone by
-- this migration or by time passing).
--
-- Fix: replace per-table xmin-based pagination with a dedicated,
-- append-only, NEVER-compressed/never-hypertable outbox table,
-- sync_changelog. Every syncable write appends one plain row here (via a
-- trigger, so xmin on THIS table -- never on activity_events -- carries
-- the commit-ordering property migration 000025 relies on), and
-- GET /v1/sync/changes paginates this single table instead of six
-- separate per-entity-table system-column scans. A plain heap table's
-- xmin is always readable regardless of what happens to the underlying
-- entity tables (compression, chunk drops, etc.), so this closes the bug
-- at its root rather than special-casing activity_events.
--
-- changelog_id is a plain identity column: a tiebreaker only (guarantees
-- global row uniqueness/insert order for humans reading the table), NOT
-- part of the pull cursor -- pagination/ordering is still the tuple
-- (xmin_xid8(sync_changelog.xmin), server_seq), exactly as migration
-- 000025 established, just anchored to this table's xmin instead of each
-- entity table's own. server_seq here is the entity row's own
-- server_seq_global-assigned value (migration 000021/000022) at the time
-- of the write that produced this changelog row, copied verbatim -- it is
-- already globally unique across every syncable table (one shared
-- sequence), so it continues to serve as the tiebreaker within a shared
-- xid8 exactly like it did when pagination lived on the entity tables
-- directly.
--
-- user_id is NULL only for a system-default category row (categories.user_id
-- IS NULL, per documentation/database-schema.md); every other entity type's
-- user_id is NOT NULL on its own table, so this column is never NULL for
-- them. event_started_at is populated ONLY for activity_events rows: that
-- table's primary key is the composite (user_id, started_at, event_id) --
-- started_at is the hypertable's partitioning column -- so a point lookup
-- against activity_events MUST include started_at to land on the right
-- chunk/partition; ordinary (non-system-column) reads against a compressed
-- chunk are fully supported, only system-column references are restricted,
-- so binding started_at here is what makes the post-compression point
-- lookup in internal/sync/pull.go safe. Every other entity type's point
-- lookup only needs (user_id, entity_id), so event_started_at is left NULL
-- for them.
CREATE TABLE sync_changelog (
    changelog_id     bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id          uuid NULL,
    entity_type      text NOT NULL,
    entity_id        uuid NOT NULL,
    event_started_at timestamptz NULL,
    server_seq       bigint NOT NULL
);

-- Matches the pull query's `WHERE (user_id = $1 OR user_id IS NULL) ...
-- ORDER BY xmin_xid8(xmin), server_seq` shape: a plain (user_id, server_seq)
-- index lets Postgres narrow to one caller's rows (plus the NULL/system
-- rows, via a bitmap-or or separate index scan) before the xmin_xid8/
-- snapshot-horizon predicate and ORDER BY are evaluated, exactly like every
-- per-entity-table (user_id, server_seq) index this migration's pull
-- rework stops using for pagination (those indexes are untouched here --
-- they remain in place and in use by each CRUD group's own GET list
-- endpoint, e.g. ListProjectsForUser, which paginates by plain server_seq
-- and is unrelated to this ticket's H1 fix).
CREATE INDEX sync_changelog_user_server_seq_idx ON sync_changelog (user_id, server_seq);

-- One trigger function per syncable table (rather than one generic
-- function keyed off TG_TABLE_NAME/TG_ARGV) because each table's row shape
-- differs: which column is the "entity id" to record (activity_events:
-- event_id; user_app_settings: app_id, since its primary key is the
-- composite (user_id, app_id) and user_id is already captured in its own
-- column; every other table: id), and whether event_started_at applies
-- (activity_events only). Each fires AFTER INSERT OR UPDATE, i.e. strictly
-- after migration 000022's BEFORE trigger has already assigned
-- NEW.server_seq, so the value copied into sync_changelog here is always
-- the row's real, final server_seq for this write -- and because both the
-- entity table's write and this changelog append happen inside the exact
-- same transaction, sync_changelog's row gets the exact same xid (commit
-- time) as the entity write it describes, which is the property this
-- migration's pull rework depends on.
CREATE FUNCTION log_activity_event_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, event_started_at, server_seq)
    VALUES (NEW.user_id, 'activity_events', NEW.event_id, NEW.started_at, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER activity_events_log_change
    AFTER INSERT OR UPDATE ON activity_events
    FOR EACH ROW EXECUTE FUNCTION log_activity_event_change();

CREATE FUNCTION log_focus_session_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
    VALUES (NEW.user_id, 'focus_sessions', NEW.id, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER focus_sessions_log_change
    AFTER INSERT OR UPDATE ON focus_sessions
    FOR EACH ROW EXECUTE FUNCTION log_focus_session_change();

CREATE FUNCTION log_project_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
    VALUES (NEW.user_id, 'projects', NEW.id, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER projects_log_change
    AFTER INSERT OR UPDATE ON projects
    FOR EACH ROW EXECUTE FUNCTION log_project_change();

CREATE FUNCTION log_tag_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
    VALUES (NEW.user_id, 'tags', NEW.id, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tags_log_change
    AFTER INSERT OR UPDATE ON tags
    FOR EACH ROW EXECUTE FUNCTION log_tag_change();

CREATE FUNCTION log_user_app_setting_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
    VALUES (NEW.user_id, 'user_app_settings', NEW.app_id, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER user_app_settings_log_change
    AFTER INSERT OR UPDATE ON user_app_settings
    FOR EACH ROW EXECUTE FUNCTION log_user_app_setting_change();

-- categories.user_id is nullable (NULL denotes a system-default category,
-- per documentation/database-schema.md); NEW.user_id is copied verbatim
-- (including NULL) so a system default's changelog row also has
-- user_id IS NULL, matching the pull query's
-- `WHERE (user_id = $1 OR user_id IS NULL)` scoping.
CREATE FUNCTION log_category_change() RETURNS trigger AS $$
BEGIN
    INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
    VALUES (NEW.user_id, 'categories', NEW.id, NEW.server_seq);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER categories_log_change
    AFTER INSERT OR UPDATE ON categories
    FOR EACH ROW EXECUTE FUNCTION log_category_change();

-- Backfill: seed one changelog row per existing syncable row across all six
-- tables, using each row's CURRENT server_seq, so a fresh sync cursor
-- (starting at the zero cursor) still discovers every row that existed
-- before this migration ran -- not just rows written after it. Every
-- backfilled row is inserted by this migration's own transaction, so they
-- all share this migration's single xid: deterministic (they all become
-- visible to a pull's horizon at the same time, once this transaction
-- commits), and harmless -- migration 000025's gap-free-by-construction
-- argument does not depend on backfilled rows having distinct xids, only
-- on the (xid8, server_seq) tuple being unique and monotonically
-- non-decreasing in commit order, which holds here since every backfilled
-- row's server_seq is already unique (server_seq_global) and shares one
-- xid.
INSERT INTO sync_changelog (user_id, entity_type, entity_id, event_started_at, server_seq)
SELECT user_id, 'activity_events', event_id, started_at, server_seq FROM activity_events;

INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
SELECT user_id, 'focus_sessions', id, server_seq FROM focus_sessions;

INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
SELECT user_id, 'projects', id, server_seq FROM projects;

INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
SELECT user_id, 'tags', id, server_seq FROM tags;

INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
SELECT user_id, 'user_app_settings', app_id, server_seq FROM user_app_settings;

INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
SELECT user_id, 'categories', id, server_seq FROM categories;

-- internal/sync/pull.go no longer queries activity_events/focus_sessions/
-- projects/tags/user_app_settings/categories for PAGINATION (it now
-- paginates sync_changelog alone, then does a plain-column point lookup
-- per entity to resolve current state -- see that package's doc comments).
-- Migrations 000024/000025's xmin_xid8/xid_before_snapshot_horizon
-- functions are NOT dropped here: they are reused, unmodified, by
-- sync_changelog's own pagination/horizon predicates below (see
-- internal/store/queries/pull.sql's ListChangelogPage), and the
-- (user_id, server_seq) indexes migrations 000023/000024 added to
-- projects/tags/user_app_settings/categories remain in active use by each
-- CRUD group's own GET list endpoint (ListProjectsForUser and friends),
-- which is a separate, plain server_seq cursor unrelated to this ticket.
-- Nothing from 000021-000025 is dead code after this migration; only
-- pull.go's USE of the per-entity xmin_xid8 columns for pagination is
-- superseded.
COMMENT ON TABLE sync_changelog IS
    'RIZ-34 (pivot): append-only sync outbox. One row per write to a syncable table (activity_events, focus_sessions, projects, tags, user_app_settings, categories), appended by a same-transaction AFTER trigger so this table''s own xmin -- never a compressed hypertable''s -- carries the commit-ordering property GET /v1/sync/changes''s pull cursor depends on. See this migration''s header comment for the full rationale and the compression bug it fixes.';
