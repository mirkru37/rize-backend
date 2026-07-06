-- Restore xid_before_snapshot_horizon to migration 000024's self-contained
-- definition (inline widening, no dependency on xmin_xid8) before dropping
-- xmin_xid8 itself.
CREATE OR REPLACE FUNCTION xid_before_snapshot_horizon(row_xmin xid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN row_xmin::text::bigint <= (pg_snapshot_xmax(pg_current_snapshot())::text::bigint & 4294967295)
            THEN ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) + row_xmin::text::bigint)::text::xid8
        ELSE ((pg_snapshot_xmax(pg_current_snapshot())::text::bigint & ~4294967295) - 4294967296 + row_xmin::text::bigint)::text::xid8
    END < pg_snapshot_xmin(pg_current_snapshot());
$$;

DROP FUNCTION IF EXISTS xmin_xid8(xid);
