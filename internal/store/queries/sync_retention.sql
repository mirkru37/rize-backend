-- name: SelectChangelogPruneBatch :many
-- One bounded batch of sync_changelog rows eligible for age-based pruning
-- (RIZ-72): every row whose created_at is strictly before cutoff (the
-- caller-computed now() - SYNC_CHANGELOG_MAX_AGE boundary), oldest
-- (changelog_id) first, capped at batch_limit so a single prune tick never
-- holds a huge result set or a long-running transaction. The caller
-- (internal/sync's retention Pruner) deletes exactly these rows and
-- advances sync_changelog_horizon from their (xid8, server_seq) tuples in
-- the SAME database transaction as this SELECT — see DeleteChangelogRows
-- and AdvanceChangelogHorizon below, and internal/sync/retention.go's
-- PruneOnce for the transactional pairing.
--
-- xmin_xid8(xmin) (migration 000025) is selected here, before the rows are
-- deleted, because it cannot be recovered afterward — once a row is gone
-- its xmin is gone with it. Capturing it here is what lets the horizon
-- advance to the exact commit-ordered position these rows occupied.
SELECT changelog_id, xmin_xid8(xmin) AS xid8, server_seq
FROM sync_changelog
WHERE created_at < sqlc.arg(cutoff)::timestamptz
ORDER BY changelog_id
LIMIT sqlc.arg(batch_limit);

-- name: DeleteChangelogRows :execrows
-- Deletes exactly the rows SelectChangelogPruneBatch identified, by their
-- changelog_id primary keys, in the SAME transaction as that select. Using
-- the exact id set (rather than re-evaluating the created_at < cutoff
-- predicate a second time) means this DELETE can never race with a
-- concurrent write that inserted a new, unrelated row after the select ran
-- but before the delete executes — it only ever touches the exact rows
-- already identified as prune candidates.
DELETE FROM sync_changelog
WHERE changelog_id = ANY(sqlc.arg(changelog_ids)::bigint[]);

-- name: AdvanceChangelogHorizon :one
-- Advances the single sync_changelog_horizon row to the maximum of its
-- current (horizon_xid8, horizon_server_seq) and the caller-supplied
-- values (the max xid8/server_seq among the batch just deleted). GREATEST
-- (rather than an unconditional SET) makes this safe to call with a batch
-- whose max happens to be behind the already-recorded horizon — which
-- cannot happen in the normal single-pruner-instance case (batches are
-- processed in changelog_id order, which is also non-decreasing in
-- (xid8, server_seq) per migration 000025's gap-free-by-construction
-- invariant) but keeps this query correct/idempotent under retry or a
-- hypothetical second pruner instance rather than relying on that
-- invariant to hold at the call site too.
UPDATE sync_changelog_horizon
SET horizon_xid8       = GREATEST(horizon_xid8, sqlc.arg(xid8)::xid8),
    horizon_server_seq  = GREATEST(horizon_server_seq, sqlc.arg(server_seq)::bigint),
    pruned_at           = now(),
    updated_at          = now()
WHERE id
RETURNING horizon_xid8, horizon_server_seq;

-- name: GetChangelogHorizon :one
-- Reads the current retained horizon, per internal/sync/pull.go's
-- cursor-reset check: a pull whose caller-supplied cursor is strictly
-- below this position cannot be served a gap-free page (rows between its
-- cursor and here were deleted by age-based retention) and must be told to
-- reset instead. Called inside the same REPEATABLE READ, READ ONLY
-- transaction as the rest of a pull (runInPullSnapshot) so the horizon
-- check and the changelog page read observe one consistent snapshot.
SELECT horizon_xid8, horizon_server_seq FROM sync_changelog_horizon WHERE id;
