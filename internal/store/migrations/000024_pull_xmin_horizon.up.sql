-- RIZ-34 (H1 fix): GET /v1/sync/changes previously paginated each syncable
-- table by `server_seq` alone. server_seq is assigned by
-- `nextval('server_seq_global')` in a BEFORE INSERT/UPDATE trigger
-- (000022), and nextval() is NOT commit-ordered: a slower transaction can
-- be handed a lower server_seq than a faster concurrent transaction that
-- commits first. A pull that only checks `server_seq > cursor` can
-- therefore observe seq 101 (already committed) while seq 100 is still
-- sitting inside an uncommitted transaction, advance next_cursor past 100,
-- and PERMANENTLY skip it once that transaction finally commits (its
-- server_seq is now behind the client's cursor forever).
--
-- The fix gates every delivered row on the row's `xmin` system column
-- (the id of the transaction that inserted/most-recently-updated it)
-- against the pulling transaction's MVCC snapshot horizon
-- (pg_snapshot_xmin(pg_current_snapshot())): a row is only delivered if
-- its xmin committed strictly before every transaction that was still
-- in-flight when the snapshot was taken. A row whose xmin is at or past
-- that horizon might belong to a transaction that hasn't committed yet
-- (or one that started after ours), so it is simply not delivered THIS
-- pull — it becomes visible (with a now-fully-committed xmin behind the
-- horizon) on a later pull once time moves the horizon past it. Because
-- next_cursor is only ever advanced over rows that passed this gate (see
-- internal/sync/pull.go), and the horizon guarantees every row with a
-- lower server_seq belonging to a transaction still in flight at snapshot
-- time is excluded from this page too, next_cursor can never advance past
-- an as-yet-uncommitted row.
--
-- xid_before_snapshot_horizon widens a row's 32-bit `xmin` (which wraps
-- around every ~4 billion transactions) into the 64-bit xid8 domain that
-- pg_current_snapshot()'s xmin/xmax bounds already live in (PG13+), so the
-- two can be compared unambiguously instead of comparing a wrapped 32-bit
-- counter directly against a 64-bit one.
--
-- Wraparound-safety of the widening: pg_snapshot_xmax(snapshot) is anchor,
-- already expressed as a full xid8 (epoch:xid). Reinterpreting xmin in
-- that same "epoch" and comparing it against anchor's low 32 bits tells us
-- which epoch xmin actually belongs to:
--   * xmin <= low32(anchor): xmin is numerically at-or-behind the anchor
--     within the same epoch — no wraparound occurred between xmin's
--     transaction and the anchor, so xmin's xid8 is (anchor's epoch, xmin).
--   * xmin >  low32(anchor): xmin is numerically AHEAD of the anchor, which
--     is only possible if the 32-bit counter wrapped between xmin's
--     transaction and the anchor — so xmin actually predates the anchor's
--     epoch by one: (anchor's epoch - 1, xmin).
-- This assumes at most one wraparound occurred between xmin and the
-- current snapshot, i.e. that the row's raw xmin is at most ~2^31
-- transactions old relative to the current xid. NOTE: this is NOT the
-- invariant Postgres's own anti-wraparound vacuuming guarantees — freezing
-- only preserves a row's VISIBILITY across wraparound, it does not bound
-- or rewrite the row's raw `xmin` value, which is what we read and use
-- here as an ORDERING key. The bound this code actually depends on is
-- external: every live sync_changelog row's raw xmin must stay within
-- 2^31 transactions of the current xid, which holds only because of the
-- changelog's retention/pruning policy (tracked as RIZ-72, not yet
-- implemented as of this migration) rather than anything Postgres
-- enforces on its own.
CREATE FUNCTION xid_before_snapshot_horizon(row_xmin xid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN row_xmin::text::bigint <= (pg_snapshot_xmax(pg_current_snapshot())::text::bigint & 4294967295)
            THEN ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) + row_xmin::text::bigint)::text::xid8
        ELSE ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) - 4294967296 + row_xmin::text::bigint)::text::xid8
    END < pg_snapshot_xmin(pg_current_snapshot());
$$;

-- RIZ-34 (M1): categories now participate in GET /v1/sync/changes's pull
-- feed (see internal/store/queries/pull.sql's ListCategoryChangesForUser),
-- keyset-paginated by (user_id, server_seq) exactly like projects/tags/
-- user_app_settings (000023). This index gives that new query the same
-- access path those already have.
CREATE INDEX categories_user_server_seq_idx ON categories (user_id, server_seq);
