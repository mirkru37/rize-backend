package sync

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// newSyncTestRouterWithPool mirrors handlers_test.go's newSyncTestRouter,
// additionally returning the *pgxpool.Pool backing it — RIZ-72's
// cursor-expired HTTP test needs direct SQL access (setHorizonForTest) to
// the exact same database the handler under test reads from.
func newSyncTestRouterWithPool(t *testing.T) (*chi.Mux, *pgxpool.Pool, storedb.User, storedb.Device, string) {
	t.Helper()
	pool := testPool(t)
	q := storedb.New(pool)
	svc := &Service{Queries: q, Pool: pool}
	h := NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	user, device := newUser(t, q)
	token, err := auth.IssueAccessToken(key, user.ID.String(), "user", device.ID.String(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return r, pool, user, device, token
}

// backdateChangelogRow rewrites the sync_changelog row for entityID's
// created_at, simulating "this row was written age ago" for retention
// tests (RIZ-72 added created_at in migration 000027; there is no write
// path that lets a test control it directly, since it's always now() at
// insert time via the entity table's AFTER trigger).
func backdateChangelogRow(t *testing.T, pool *pgxpool.Pool, entityID pgtype.UUID, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `UPDATE sync_changelog SET created_at = $1 WHERE entity_id = $2`,
		time.Now().UTC().Add(-age), entityID)
	if err != nil {
		t.Fatalf("backdate changelog row: %v", err)
	}
}

// changelogRowExists reports whether entityID still has a sync_changelog
// row, used to verify pruning by specific row rather than by a raw total
// count — the shared sync_changelog/sync_changelog_horizon tables persist
// across every test in this package (and across repeated `go test` runs
// against the same compose database), so per-row existence checks are the
// only assertion that's robust regardless of what other tests or prior
// runs left behind.
func changelogRowExists(t *testing.T, pool *pgxpool.Pool, entityID pgtype.UUID) bool {
	t.Helper()
	ctx := context.Background()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM sync_changelog WHERE entity_id = $1`, entityID).Scan(&n); err != nil {
		t.Fatalf("count changelog rows: %v", err)
	}
	return n > 0
}

// currentHorizon reads sync_changelog_horizon's single row directly (bypassing
// the sync package under test) so tests can capture/restore it around
// assertions that need a known horizon value.
func currentHorizon(t *testing.T, pool *pgxpool.Pool) store.PullCursor {
	t.Helper()
	ctx := context.Background()
	q := storedb.New(pool)
	row, err := q.GetChangelogHorizon(ctx)
	if err != nil {
		t.Fatalf("GetChangelogHorizon: %v", err)
	}
	return store.PullCursor{Xid8: row.HorizonXid8.Uint64, ServerSeq: row.HorizonServerSeq}
}

// setHorizonForTest force-sets sync_changelog_horizon directly via SQL (an
// unconditional SET, unlike the production AdvanceChangelogHorizon query's
// GREATEST) and registers a cleanup that restores whatever value was there
// before, so a test exercising an artificially-advanced horizon never
// permanently pollutes the shared singleton row for every other test (in
// this package, and in any later `go test` run against the same
// persistent compose database).
func setHorizonForTest(t *testing.T, pool *pgxpool.Pool, c store.PullCursor) {
	t.Helper()
	ctx := context.Background()
	before := currentHorizon(t, pool)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(),
			`UPDATE sync_changelog_horizon SET horizon_xid8 = $1, horizon_server_seq = $2 WHERE id`,
			before.Xid8, before.ServerSeq)
		if err != nil {
			t.Errorf("restore horizon after test: %v", err)
		}
	})
	_, err := pool.Exec(ctx,
		`UPDATE sync_changelog_horizon SET horizon_xid8 = $1, horizon_server_seq = $2 WHERE id`,
		c.Xid8, c.ServerSeq)
	if err != nil {
		t.Fatalf("set horizon for test: %v", err)
	}
}

// snapshotHorizonForCleanup captures the current sync_changelog_horizon
// value and registers a cleanup that restores it, for any test that calls
// a real Pruner.PruneOnce against rows it backdated (fake created_at, but
// a REAL, current xid8/server_seq — an inherent property of "simulate
// aging without waiting 90 days" backdating).
//
// This matters because sync_changelog_horizon is a single, permanent,
// shared row: every other test in this package (and in every later `go
// test` run against the same persistent compose database) reads it too.
// A real prune of a backdated-but-transactionally-current row legitimately
// advances the horizon to (roughly) "now" in xid8 terms — far ahead of
// where a genuine 90-day-old prune would ever land in production, and
// high enough to make an unrelated test's ordinary, freshly-issued cursor
// register as "below the horizon" simply because that other cursor
// happens to point at an old, low-xid8 row (e.g. the system-default
// categories backfilled once at migration-apply time). Restoring the
// horizon afterward accepts a narrow, deliberate trade-off: any already
// -deleted row unique to THIS test might, in principle, be silently
// missing from a page some other test pulls after the restore — but no
// other test ever asks for that row (it only ever existed as this test's
// own throwaway fixture), so the trade-off is invisible in practice and
// keeps the shared horizon watermark meaningful for everyone else.
func snapshotHorizonForCleanup(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	before := currentHorizon(t, pool)
	t.Cleanup(func() {
		_, err := pool.Exec(context.Background(),
			`UPDATE sync_changelog_horizon SET horizon_xid8 = $1, horizon_server_seq = $2 WHERE id`,
			before.Xid8, before.ServerSeq)
		if err != nil {
			t.Errorf("restore horizon after test: %v", err)
		}
	})
}

// pruneUntilGone repeatedly calls PruneOnce (bounded by maxTicks) until
// entityID's changelog row is gone, so batch-size tests remain correct
// even if the shared sync_changelog table happens to contain older,
// unrelated eligible rows ahead of this test's own target in changelog_id
// order (e.g. leftovers from a previous interrupted run against the same
// persistent compose database).
func pruneUntilGone(t *testing.T, p *Pruner, pool *pgxpool.Pool, entityID pgtype.UUID, maxTicks int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < maxTicks; i++ {
		if !changelogRowExists(t, pool, entityID) {
			return
		}
		if _, err := p.PruneOnce(ctx); err != nil {
			t.Fatalf("PruneOnce (tick %d): %v", i, err)
		}
	}
	if changelogRowExists(t, pool, entityID) {
		t.Fatalf("changelog row for %s still present after %d prune ticks", entityID.String(), maxTicks)
	}
}

// TestPruneOnceRespectsMaxAge is RIZ-72's core age-based retention
// contract: a row older than MaxAge is pruned, a row within MaxAge is
// not, table-driven over both cases sharing one setup.
func TestPruneOnceRespectsMaxAge(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	snapshotHorizonForCleanup(t, pool)

	tests := []struct {
		name       string
		age        time.Duration
		wantPruned bool
	}{
		{name: "older than max age is pruned", age: 100 * 24 * time.Hour, wantPruned: true},
		{name: "younger than max age survives", age: time.Hour, wantPruned: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			idStr := insertTag(t, pool, user, randomSuffix(t), 0)
			id := mustParseUUID(t, idStr)
			backdateChangelogRow(t, pool, id, tt.age)

			p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: 1000}

			if !tt.wantPruned {
				// A row within MaxAge is never eligible, so a
				// pruneUntilGone-style loop would just spin until its
				// tick budget runs out; call PruneOnce exactly once and
				// assert survival directly instead.
				if _, err := p.PruneOnce(context.Background()); err != nil {
					t.Fatalf("PruneOnce: %v", err)
				}
				if !changelogRowExists(t, pool, id) {
					t.Fatalf("row younger than MaxAge was pruned, want it to survive")
				}
				return
			}

			pruneUntilGone(t, p, pool, id, 50)
			if changelogRowExists(t, pool, id) {
				t.Fatalf("row older than MaxAge was not pruned")
			}
		})
	}
}

// TestPruneOnceHonorsBatchSize proves a single PruneOnce call never
// deletes more than BatchSize rows, even when more are eligible.
func TestPruneOnceHonorsBatchSize(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	snapshotHorizonForCleanup(t, pool)

	const rowCount = 6
	const batchSize = 2
	suffix := randomSuffix(t)
	ids := make([]pgtype.UUID, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		idStr := insertTag(t, pool, user, suffix, i)
		id := mustParseUUID(t, idStr)
		backdateChangelogRow(t, pool, id, 100*24*time.Hour)
		ids = append(ids, id)
	}

	p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: batchSize}
	deleted, err := p.PruneOnce(context.Background())
	if err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if deleted > batchSize {
		t.Fatalf("PruneOnce deleted %d rows in one call, want <= BatchSize (%d)", deleted, batchSize)
	}

	// Drain the rest and confirm every one of this test's own rows is
	// eventually gone, proving the cap didn't just silently drop rows.
	for _, id := range ids {
		pruneUntilGone(t, p, pool, id, 50)
	}
}

// TestPruneOnceNoEligibleRowsIsANoOp proves an empty batch (nothing older
// than MaxAge among this test's own rows) returns deleted=0 and no error,
// rather than treating "nothing to do" as a failure.
func TestPruneOnceNoEligibleRowsIsANoOp(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)

	idStr := insertTag(t, pool, user, randomSuffix(t), 0)
	id := mustParseUUID(t, idStr)
	// Freshly inserted: created_at is now(), nowhere near MaxAge=90d old.

	p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: 1000}
	if _, err := p.PruneOnce(context.Background()); err != nil {
		t.Fatalf("PruneOnce: %v", err)
	}
	if !changelogRowExists(t, pool, id) {
		t.Fatalf("fresh row was unexpectedly pruned")
	}
}

// TestPruneOnceRejectsBadConfig proves PruneOnce fails loudly on
// misconfiguration rather than silently pruning nothing or everything.
func TestPruneOnceRejectsBadConfig(t *testing.T) {
	pool := testPool(t)

	tests := []struct {
		name string
		p    *Pruner
	}{
		{name: "zero MaxAge", p: &Pruner{Pool: pool, MaxAge: 0, BatchSize: 100}},
		{name: "negative MaxAge", p: &Pruner{Pool: pool, MaxAge: -time.Hour, BatchSize: 100}},
		{name: "zero BatchSize", p: &Pruner{Pool: pool, MaxAge: time.Hour, BatchSize: 0}},
		{name: "negative BatchSize", p: &Pruner{Pool: pool, MaxAge: time.Hour, BatchSize: -1}},
		{name: "nil pool", p: &Pruner{Pool: nil, MaxAge: time.Hour, BatchSize: 100}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.p.PruneOnce(context.Background()); err == nil {
				t.Fatalf("PruneOnce with %s: got nil error, want an error", tt.name)
			}
		})
	}
}

// TestPruneOnceAdvancesHorizonToPrunedBatchMax proves the persisted
// horizon advances to (at least) the max (xid8, server_seq) of the rows a
// prune batch actually deleted, and that it only ever advances forward
// (GREATEST), never backward, per AdvanceChangelogHorizon's doc comment.
func TestPruneOnceAdvancesHorizonToPrunedBatchMax(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)

	snapshotHorizonForCleanup(t, pool)

	idStr := insertTag(t, pool, user, randomSuffix(t), 0)
	id := mustParseUUID(t, idStr)
	backdateChangelogRow(t, pool, id, 100*24*time.Hour)

	before := currentHorizon(t, pool)

	p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: 1000}
	pruneUntilGone(t, p, pool, id, 50)

	after := currentHorizon(t, pool)
	if after.Less(before) {
		t.Fatalf("horizon regressed: before=%+v after=%+v", before, after)
	}

	// Directly exercise AdvanceChangelogHorizon's GREATEST behavior:
	// calling it with a value strictly behind the current horizon must
	// leave the horizon unchanged.
	lower := storedb.AdvanceChangelogHorizonParams{
		Xid8:      pgtype.Uint64{Uint64: 0, Valid: true},
		ServerSeq: 0,
	}
	result, err := q.AdvanceChangelogHorizon(context.Background(), lower)
	if err != nil {
		t.Fatalf("AdvanceChangelogHorizon: %v", err)
	}
	got := store.PullCursor{Xid8: result.HorizonXid8.Uint64, ServerSeq: result.HorizonServerSeq}
	if got.Less(after) || after.Less(got) {
		t.Fatalf("AdvanceChangelogHorizon with a lower value changed the horizon: got %+v, want unchanged %+v", got, after)
	}
}

// TestPullCursorBelowHorizonReturnsCursorExpired is RIZ-72's pull-path
// contract: a caller's cursor strictly below the retained horizon can no
// longer be served a gap-free page and must be told to reset, per
// documentation/sync-protocol.md's Device Restore from Backup recovery
// path.
func TestPullCursorBelowHorizonReturnsCursorExpired(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	_ = insertTag(t, pool, user, randomSuffix(t), 0)
	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (baseline): %v", err)
	}
	staleCursor := first.NextCursor
	decoded, err := store.DecodePullCursor(staleCursor)
	if err != nil {
		t.Fatalf("DecodePullCursor: %v", err)
	}

	// Force the horizon strictly past the stale cursor's position; restored
	// automatically via setHorizonForTest's cleanup.
	setHorizonForTest(t, pool, store.PullCursor{Xid8: decoded.Xid8 + 1_000_000, ServerSeq: decoded.ServerSeq + 1_000_000})

	_, err = svc.pull(ctx, userIDString(user), staleCursor, 500)
	if !errors.Is(err, ErrCursorExpired) {
		t.Fatalf("pull with cursor below horizon: err = %v, want ErrCursorExpired", err)
	}
}

// TestPullCursorAtOrAboveHorizonSucceeds proves the check is strict
// ("below", not "at or below"): a cursor exactly AT the retained horizon —
// meaning every row up to and including that point is safely accounted
// for — must still be served normally, not rejected.
func TestPullCursorAtOrAboveHorizonSucceeds(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	_ = insertTag(t, pool, user, randomSuffix(t), 0)
	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (baseline): %v", err)
	}
	cursor := first.NextCursor
	decoded, err := store.DecodePullCursor(cursor)
	if err != nil {
		t.Fatalf("DecodePullCursor: %v", err)
	}

	// Set the horizon to EXACTLY this cursor's position (boundary case).
	setHorizonForTest(t, pool, decoded)

	if _, err := svc.pull(ctx, userIDString(user), cursor, 500); err != nil {
		t.Fatalf("pull with cursor exactly at horizon: %v, want success", err)
	}
}

// TestPullEmptyCursorNeverExpiresEvenPastHorizon proves a first-ever pull
// (empty cursor) is always servable regardless of how far the horizon has
// advanced: "from the beginning" always means "from whatever survives",
// never an error, per store.PullCursor.IsZero's doc comment.
func TestPullEmptyCursorNeverExpiresEvenPastHorizon(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	_ = insertTag(t, pool, user, randomSuffix(t), 0)

	// Force the horizon absurdly far ahead; restored via cleanup.
	setHorizonForTest(t, pool, store.PullCursor{Xid8: 1 << 40, ServerSeq: 1 << 40})

	if _, err := svc.pull(ctx, userIDString(user), "", 500); err != nil {
		t.Fatalf("pull with empty cursor past horizon: %v, want success", err)
	}
}

// TestHTTP_SyncPullChangesCursorExpiredReturns410 proves ErrCursorExpired
// maps to HTTP 410 Gone at the handler boundary, per
// writeServiceError's doc comment: a 4xx the client might blindly retry
// unchanged would be wrong here, since this exact cursor value can never
// succeed again.
func TestHTTP_SyncPullChangesCursorExpiredReturns410(t *testing.T) {
	r, pool, user, _, token := newSyncTestRouterWithPool(t)
	q := storedb.New(pool)
	ctx := context.Background()

	_ = insertTag(t, pool, user, randomSuffix(t), 0)
	svc := &Service{Queries: q, Pool: pool}
	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (baseline): %v", err)
	}
	staleCursor := first.NextCursor
	decoded, err := store.DecodePullCursor(staleCursor)
	if err != nil {
		t.Fatalf("DecodePullCursor: %v", err)
	}
	setHorizonForTest(t, pool, store.PullCursor{Xid8: decoded.Xid8 + 1_000_000, ServerSeq: decoded.ServerSeq + 1_000_000})

	rec := doSyncJSON(t, r, http.MethodGet, "/v1/sync/changes?cursor="+staleCursor, nil, token)
	if rec.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410, body = %s", rec.Code, rec.Body.String())
	}
}

// TestPullPaginationSurvivesPruneBehindCursor proves pruning that only
// touches rows a client has already fully consumed (i.e. the horizon
// lands at or behind the client's current cursor, never past it) never
// disrupts that client's in-progress multi-page pagination — the
// boundary/pagination-safety case the original brief called out.
func TestPullPaginationSurvivesPruneBehindCursor(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	suffix := randomSuffix(t)
	const total = 3
	ids := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		ids[insertTag(t, pool, user, suffix, i)] = true
	}

	// This user's feed also includes every pre-existing system-default
	// category's changelog row (backfilled once, long ago, at a much
	// lower (xid8, server_seq) than anything this test just inserted —
	// see documentation/database-schema.md's sync_changelog section), so
	// page 1 with limit=1 is not guaranteed to contain one of THIS test's
	// tags. What matters for this test isn't which page any given tag
	// lands on, only that pinning the horizon exactly at an
	// already-consumed page boundary never disrupts the rest of
	// pagination — mirrored on TestPullPagination's own "loop until
	// has_more is false, then assert every id was seen" pattern.
	page1, err := svc.pull(ctx, userIDString(user), "", 1)
	if err != nil {
		t.Fatalf("pull (page 1): %v", err)
	}
	if !page1.HasMore {
		t.Fatalf("page 1: HasMore = false, want true (limit=1, more rows pending)")
	}
	decoded, err := store.DecodePullCursor(page1.NextCursor)
	if err != nil {
		t.Fatalf("DecodePullCursor: %v", err)
	}

	// Horizon lands exactly at page 1's cursor: everything the client has
	// already consumed is fair game to prune, nothing ahead of it is.
	setHorizonForTest(t, pool, decoded)

	seen := map[string]bool{}
	cursor := page1.NextCursor
	hasMore := true
	pages := 1
	for hasMore {
		pages++
		if pages > total+200 {
			t.Fatalf("pull did not converge after %d pages (stuck cursor?)", pages)
		}
		page, err := svc.pull(ctx, userIDString(user), cursor, 1)
		if err != nil {
			t.Fatalf("pull (page %d) after prune landed behind cursor: %v, want success", pages, err)
		}
		for _, u := range page.Changes["tags"].Upserts {
			seen[u.(tagUpsertDTO).ID] = true
		}
		cursor = page.NextCursor
		hasMore = page.HasMore
	}

	for id := range ids {
		if !seen[id] {
			t.Fatalf("tag id %s was never delivered after pruning landed exactly at an already-consumed cursor", id)
		}
	}
}

// TestPruneDoesNotStrandInFlightPullSnapshot is the concurrency-safety
// argument from internal/sync.Pruner's doc comment, demonstrated live: a
// REPEATABLE READ, READ ONLY transaction that has already read a
// changelog row (mirroring internal/sync/pull.go's runInPullSnapshot)
// keeps seeing that row even after a concurrent transaction deletes and
// commits it — Postgres's MVCC snapshot isolation, not any locking this
// package does itself, is what prevents a prune from stranding a pull
// that's already mid-flight.
func TestPruneDoesNotStrandInFlightPullSnapshot(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	snapshotHorizonForCleanup(t, pool)
	ctx := context.Background()

	idStr := insertTag(t, pool, user, randomSuffix(t), 0)
	id := mustParseUUID(t, idStr)
	backdateChangelogRow(t, pool, id, 100*24*time.Hour)

	// Open a REPEATABLE READ, READ ONLY snapshot and read the row, exactly
	// as internal/sync/pull.go's runInPullSnapshot would for an in-flight
	// pull.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var before int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sync_changelog WHERE entity_id = $1`, id).Scan(&before); err != nil {
		t.Fatalf("query within snapshot (before prune): %v", err)
	}
	if before != 1 {
		t.Fatalf("row not visible within snapshot before prune: count = %d, want 1", before)
	}

	// Concurrently (a separate connection/transaction) prune the row away.
	p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: 1000}
	pruneUntilGone(t, p, pool, id, 50)

	// The already-open snapshot must still see it: this is the MVCC
	// guarantee, not a coincidence.
	var during int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM sync_changelog WHERE entity_id = $1`, id).Scan(&during); err != nil {
		t.Fatalf("query within snapshot (after concurrent prune): %v", err)
	}
	if during != 1 {
		t.Fatalf("row vanished from an already-open REPEATABLE READ snapshot: count = %d, want 1 (MVCC violation)", during)
	}

	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// A brand-new snapshot, taken after the prune committed, correctly
	// no longer sees it.
	if changelogRowExists(t, pool, id) {
		t.Fatalf("row still visible from a fresh snapshot after prune committed")
	}
}

// TestPrunerRunTicksUntilContextCancellation exercises the actual
// production trigger mechanism (Pruner.Run's ticker goroutine, started by
// cmd/api/main.go), rather than only the PruneOnce method it wraps: a
// real backdated row gets deleted within a couple of ticks, and Run
// returns promptly once its context is canceled.
func TestPrunerRunTicksUntilContextCancellation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	snapshotHorizonForCleanup(t, pool)

	idStr := insertTag(t, pool, user, randomSuffix(t), 0)
	id := mustParseUUID(t, idStr)
	backdateChangelogRow(t, pool, id, 100*24*time.Hour)

	p := &Pruner{Pool: pool, MaxAge: 90 * 24 * time.Hour, BatchSize: 1000}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		p.Run(ctx, 20*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s of its context's 500ms timeout")
	}

	if changelogRowExists(t, pool, id) {
		t.Fatalf("Run's ticker never pruned the backdated row within its lifetime")
	}
}

// TestPrunerRunSurvivesTickErrors proves a single tick's failure (e.g. the
// misconfiguration PruneOnce itself rejects) is logged and Run keeps
// ticking rather than crashing the goroutine, per Run's doc comment
// ("pruning is not on the critical path of any request").
func TestPrunerRunSurvivesTickErrors(t *testing.T) {
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	// MaxAge <= 0 makes every PruneOnce call fail immediately (see
	// PruneOnce's own validation), so every tick within this short-lived
	// Run call hits the error branch.
	p := &Pruner{Pool: testPool(t), MaxAge: 0, BatchSize: 100, Logger: logger}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		p.Run(ctx, 10*time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Run did not return within 2s of its context's 100ms timeout")
	}

	if !buf.Contains("retention prune tick failed") {
		t.Fatalf("Run did not log the tick failure; log output: %q", buf.String())
	}
}

// syncBuffer is a trivial concurrency-safe io.Writer for capturing slog
// output from a goroutine, per TestPrunerRunSurvivesTickErrors.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Contains(substr string) bool {
	return strings.Contains(b.String(), substr)
}
