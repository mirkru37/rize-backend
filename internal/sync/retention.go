package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// Pruner implements RIZ-72's sync_changelog retention: an amortized,
// in-process periodic job (a ticker goroutine started by cmd/api/main.go,
// per the original brief's "amortized on-write or a periodic background
// job" choice — a background job was picked over amortizing pruning onto
// every push/pull request, since pruning is unrelated to any individual
// client's request latency and batching it into a fixed-cadence job keeps
// every push/pull request's own latency independent of how much history
// happens to be due for deletion at that moment) that deletes
// sync_changelog rows older than MaxAge in bounded batches and advances
// the persisted retention horizon (sync_changelog_horizon) to match.
//
// Concurrency/safety model (see PruneOnce for the mechanics):
//
//   - Exactly one Pruner is expected to run per deployment (the ticker
//     goroutine cmd/api/main.go starts). Nothing here takes an advisory
//     lock to enforce that; AdvanceChangelogHorizon's use of GREATEST
//     keeps a hypothetical second concurrent pruner from ever moving the
//     horizon backwards, but two pruners racing on the SAME batch of rows
//     is not a scenario this ticket needed to solve (single-process
//     deployment today; see cmd/api/main.go's doc comment).
//   - A pull that is already mid-transaction (internal/sync/pull.go's
//     runInPullSnapshot opens a REPEATABLE READ, READ ONLY transaction)
//     is never affected by a concurrent prune's DELETE, regardless of
//     exactly when that DELETE commits relative to the pull's own
//     snapshot: Postgres's MVCC guarantees a row deleted by a transaction
//     that commits AFTER a reader's snapshot was taken remains fully
//     visible to that reader until the reader's transaction ends (and
//     autovacuum never reclaims a tuple that some open snapshot might
//     still need). A pull whose snapshot is taken AFTER the prune's
//     DELETE has already committed simply never sees those rows in the
//     first place — consistent either way, never a torn read.
//   - The remaining risk isn't a single in-flight pull, it's a client
//     mid-way through a has_more=true multi-page pagination loop, where
//     each page is its own transaction/snapshot: could a prune batch
//     advance the horizon past a client's next_cursor in the gap between
//     two of that client's own page requests? Only if SYNC_CHANGELOG_
//     MAX_AGE elapses within that gap, which cannot happen for an
//     actively-paginating client (successive page requests are
//     seconds/minutes apart, not most of MaxAge) — this is precisely the
//     "long-dormant device" case the cursor-reset check (pull.go, see
//     store.PullCursor.Less/ErrCursorExpired) exists to catch instead.
type Pruner struct {
	// Queries runs every prune-related query (select batch, delete batch,
	// advance horizon) inside one explicit transaction per tick — see
	// PruneOnce. Must be backed by a real *pgxpool.Pool in production; a
	// Pruner is simply not started when Pool is nil (see cmd/api/main.go).
	Pool Beginner

	// MaxAge is how old a sync_changelog row (by created_at) must be
	// before it's eligible for deletion. Required; PruneOnce returns an
	// error if it is zero, so misconfiguration fails loudly rather than
	// pruning everything (MaxAge=0 would make "created_at < now()" match
	// every row ever written, including ones from a pull still in
	// progress).
	MaxAge time.Duration

	// BatchSize bounds how many rows one PruneOnce call deletes.
	// Required; PruneOnce returns an error if it is <= 0.
	BatchSize int

	// Logger receives one Info line per tick reporting how many rows were
	// deleted (0 included, at Debug level, to avoid log spam on a healthy,
	// already-pruned table — see Run). Defaults to slog.Default() if nil.
	Logger *slog.Logger

	// now is overridable in tests for deterministic cutoff computation;
	// defaults to time.Now when nil.
	now func() time.Time
}

func (p *Pruner) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now().UTC()
}

func (p *Pruner) logger() *slog.Logger {
	if p.Logger != nil {
		return p.Logger
	}
	return slog.Default()
}

// PruneOnce deletes up to BatchSize sync_changelog rows older than MaxAge
// and advances sync_changelog_horizon to match, all inside a single
// database transaction (per the original brief's requirement 2: "the
// retained horizon ... updated transactionally WITH each prune batch").
// It returns the number of rows deleted (0 is a normal, non-error result:
// "nothing was due for pruning this tick").
//
// Selecting, deleting, and advancing the horizon in one transaction is
// what makes this atomic from any observer's point of view: no other
// transaction can ever see the horizon advanced past a batch that hasn't
// actually been deleted yet (or vice versa) — a horizon advance and its
// corresponding delete either both become visible together at this
// transaction's commit, or (on any error) neither does, per the rollback
// path below.
func (p *Pruner) PruneOnce(ctx context.Context) (int64, error) {
	if p.MaxAge <= 0 {
		return 0, fmt.Errorf("sync: retention pruner misconfigured: MaxAge must be positive")
	}
	if p.BatchSize <= 0 {
		return 0, fmt.Errorf("sync: retention pruner misconfigured: BatchSize must be positive")
	}
	if p.Pool == nil {
		return 0, fmt.Errorf("sync: retention pruner misconfigured: Pool is nil")
	}

	tx, err := p.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("sync: begin prune transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()

	q := storedb.New(tx)
	cutoff := p.clock().Add(-p.MaxAge)

	victims, err := q.SelectChangelogPruneBatch(ctx, storedb.SelectChangelogPruneBatchParams{
		Cutoff:     pgtype.Timestamptz{Time: cutoff, Valid: true},
		BatchLimit: int32(p.BatchSize), //nolint:gosec // BatchSize is validated positive above and comes from config, not untrusted input
	})
	if err != nil {
		return 0, fmt.Errorf("sync: select prune batch: %w", err)
	}
	if len(victims) == 0 {
		// Nothing to do: no horizon advance, no delete, but still commit
		// the (read-only, no-op) transaction cleanly rather than leaving
		// it to the deferred rollback — either is equivalent here, commit
		// just avoids relying on the rollback path for the common case.
		if err := tx.Commit(ctx); err != nil {
			return 0, fmt.Errorf("sync: commit empty prune transaction: %w", err)
		}
		committed = true
		return 0, nil
	}

	ids := make([]int64, 0, len(victims))
	var maxXid8 pgtype.Uint64
	var maxServerSeq int64
	for _, v := range victims {
		ids = append(ids, v.ChangelogID)
		if !maxXid8.Valid || v.Xid8.Uint64 > maxXid8.Uint64 {
			maxXid8 = v.Xid8
		}
		if v.ServerSeq > maxServerSeq {
			maxServerSeq = v.ServerSeq
		}
	}

	deleted, err := q.DeleteChangelogRows(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("sync: delete prune batch: %w", err)
	}

	if _, err := q.AdvanceChangelogHorizon(ctx, storedb.AdvanceChangelogHorizonParams{
		Xid8:      maxXid8,
		ServerSeq: maxServerSeq,
	}); err != nil {
		return 0, fmt.Errorf("sync: advance changelog horizon: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("sync: commit prune transaction: %w", err)
	}
	committed = true

	return deleted, nil
}

// Run ticks every interval until ctx is done, calling PruneOnce on each
// tick and logging the outcome. It never returns an error: a single
// tick's failure (e.g. a transient DB blip) is logged and Run simply waits
// for the next tick rather than crashing the whole API process over a
// background maintenance job — pruning is not on the critical path of any
// request. Intended to be started as its own goroutine (see
// cmd/api/main.go).
func (p *Pruner) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			deleted, err := p.PruneOnce(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}
				p.logger().Error("sync_changelog retention prune tick failed", "error", err)
				continue
			}
			if deleted > 0 {
				p.logger().Info("sync_changelog retention prune tick", "deleted", deleted)
			} else {
				p.logger().Debug("sync_changelog retention prune tick: nothing to prune")
			}
		}
	}
}
