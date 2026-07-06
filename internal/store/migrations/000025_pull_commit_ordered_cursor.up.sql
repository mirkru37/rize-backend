-- RIZ-34 (H1 re-review fix): migration 000024's xmin-horizon gate excludes
-- any row whose transaction might still be in flight, but it left
-- server_seq (assigned by nextval() in a BEFORE INSERT/UPDATE trigger,
-- 000022) as the sole keyset-pagination/ordering key. nextval() and xid
-- assignment (XidGenLock, taken at a transaction's FIRST write) are two
-- independent counters that can be incremented in either relative order by
-- concurrent transactions: an in-flight transaction can be assigned a
-- LOWER server_seq than a transaction that started later but committed
-- first with a HIGHER xid. Concretely (reproduced live): committed
-- row seq=2061/xmin=4031 is delivered while in-flight row seq=2060/xmin=4032
-- is still open; migration 000024's horizon gate correctly EXCLUDES the
-- in-flight row from the page (its xid8 is not below the snapshot
-- horizon), but next_cursor is still computed from server_seq alone, so it
-- advances to (at least) 2061 — strictly past seq=2060 — and seq=2060 can
-- never satisfy a future page's "server_seq > cursor" predicate once it
-- finally commits. The row is permanently skipped even though the horizon
-- gate did its job.
--
-- The fix: make the pagination/ordering key the tuple
-- (xid8-widened row xmin, server_seq) instead of server_seq alone, so a
-- row's position in the keyset is anchored to the SAME xid8 value the
-- horizon gate already uses to decide "safely committed," rather than to
-- an independently-assigned counter that can race it.
--
-- xmin_xid8 factors migration 000024's inline widening arithmetic out of
-- xid_before_snapshot_horizon into its own reusable STABLE function so
-- pull.sql's SELECT/WHERE/ORDER BY clauses can all reference the row's
-- actual widened xid8 value (not just the horizon boolean). The widening
-- math itself, and its wraparound-safety argument, are unchanged from
-- migration 000024 — see that migration's comment for the full derivation;
-- it is reproduced here only enough to keep this function self-contained.
CREATE FUNCTION xmin_xid8(row_xmin xid) RETURNS xid8
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN row_xmin::text::bigint <= (pg_snapshot_xmax(pg_current_snapshot())::text::bigint & 4294967295)
            THEN ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) + row_xmin::text::bigint)::text::xid8
        ELSE ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) - 4294967296 + row_xmin::text::bigint)::text::xid8
    END;
$$;

-- xid_before_snapshot_horizon is now expressed in terms of xmin_xid8 so
-- there is exactly one place the widening arithmetic lives. Its meaning is
-- unchanged: true iff row_xmin's transaction committed strictly before
-- every transaction that was still in-flight when the current snapshot was
-- taken (see migration 000024 for the full invariant).
CREATE OR REPLACE FUNCTION xid_before_snapshot_horizon(row_xmin xid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT xmin_xid8(row_xmin) < pg_snapshot_xmin(pg_current_snapshot());
$$;

-- Gap-free-by-construction invariant for the new (xid8, server_seq) keyset:
-- the horizon gate (xid_before_snapshot_horizon / xmin_xid8(...) <
-- pg_snapshot_xmin(...)) only ever admits rows whose xid8 is BELOW the
-- current snapshot's horizon — i.e. rows whose inserting/last-updating
-- transaction is permanently, fully committed and can never be superseded
-- by an as-yet-uncommitted transaction with a lower xid8 (transaction ids
-- are assigned monotonically and never reused). That settled prefix
-- (every row with xid8 < horizon) is therefore APPEND-ONLY under
-- (xid8, server_seq) order: once a row's xid8 is below some horizon value,
-- it stays below every later, larger horizon value forever, and no row can
-- ever be inserted "behind" it in xid8 order after the fact (xid8 only
-- increases). Ordering the keyset by (xid8, server_seq) instead of
-- server_seq alone means a page's upper boundary is always a real,
-- permanent position in that append-only prefix, independent of
-- server_seq's assignment order — so keyset pagination over it cannot skip
-- a row regardless of how nextval() interleaves with commit order. (This
-- is the same reasoning migration 000024 applied to the horizon check
-- alone; it now also justifies using xid8 as the leading pagination key.)
COMMENT ON FUNCTION xmin_xid8(xid) IS
    'RIZ-34: widens a row''s 32-bit xmin into the 64-bit xid8 domain so it can serve as the leading key of the (xid8, server_seq) commit-ordered pull cursor. See migration 000025''s comment for the gap-free-by-construction invariant this enables.';
