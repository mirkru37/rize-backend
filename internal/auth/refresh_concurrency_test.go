package auth_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// testDBPool returns a pool connected to DATABASE_URL, or skips the test
// when DATABASE_URL is unset, mirroring internal/store/store_test.go's
// testPool helper (unexported there, so duplicated here for
// internal/auth's DB-backed tests). RIZ-32 M2's atomic-refresh-rotation fix
// can only be meaningfully exercised against a real Postgres row lock, not
// the in-memory fakeQuerier used by the rest of this package's tests.
func testDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping DB-backed auth integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := store.NewPool(ctx, dsn)
	if err != nil {
		t.Fatalf("store.NewPool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("pool.Ping: %v", err)
	}

	t.Cleanup(pool.Close)
	return pool
}

func newDBBackedTestService(t *testing.T, pool *pgxpool.Pool) *auth.Service {
	t.Helper()
	return &auth.Service{
		Queries:    storedb.New(pool),
		Pool:       pool,
		SigningKey: testSigningKey(t),
	}
}

// TestRefreshConcurrentRace_OneWinnerOneCleanLoser exercises RIZ-32 M2's
// tx-based Refresh path against a real database: two goroutines race to
// refresh the exact same token. Per the fix's requirements, exactly one
// must win with a valid new token, the other must get a clean 401
// (ErrInvalidRefreshToken, NOT ErrRefreshTokenReuse — losing a race is not
// evidence of token theft), the winner's new token must remain usable
// afterward, and the token family must not have been revoked.
func TestRefreshConcurrentRace_OneWinnerOneCleanLoser(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("racer"), "correct-horse-battery-staple", testDevice("Racer's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	token := registered.Tokens.RefreshToken

	// Local, same-process goroutines racing against a local database can
	// complete a whole transaction (begin -> NOWAIT lock -> create ->
	// rotate -> commit) fast enough that the second goroutine's own lock
	// attempt only reaches Postgres *after* the first has already
	// committed and released the lock — at that point there genuinely was
	// no contention, and being classified as reuse would be correct (see
	// TestRefreshGenuineReuse_SequentialStaleTokenRevokesFamily for that
	// case), not a test bug. To reliably exercise the *actually concurrent*
	// path this test targets, we first take the row lock ourselves from a
	// separate, deliberately-held transaction, so both goroutines'
	// Refresh calls are guaranteed to observe contention (their initial
	// FOR UPDATE NOWAIT attempts fail against our held lock) before we
	// release it and let them race each other for the real lock.
	holdTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock-holding transaction: %v", err)
	}
	var heldID any
	if err := holdTx.QueryRow(ctx,
		`SELECT id FROM refresh_tokens WHERE device_id = $1 AND revoked_at IS NULL FOR UPDATE`,
		registered.Device.ID,
	).Scan(&heldID); err != nil {
		t.Fatalf("acquire holding lock: %v", err)
	}

	const attempts = 2
	type outcome struct {
		result auth.Result
		err    error
	}
	results := make([]outcome, attempts)

	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.Refresh(ctx, token, nil)
			results[i] = outcome{result: res, err: err}
		}(i)
	}

	// Give both goroutines time to reach Postgres, hit our held lock with
	// FOR UPDATE NOWAIT (observing contention), and move on to the
	// blocking FOR UPDATE wait, before we release the lock they're queued
	// behind.
	time.Sleep(200 * time.Millisecond)
	if err := holdTx.Rollback(ctx); err != nil {
		t.Fatalf("release holding lock: %v", err)
	}

	wg.Wait()

	var wins, cleanLosses, reuseLosses, otherErrs int
	var winnerToken string
	for _, o := range results {
		switch {
		case o.err == nil:
			wins++
			winnerToken = o.result.Tokens.RefreshToken
		case errors.Is(o.err, auth.ErrInvalidRefreshToken):
			cleanLosses++
		case errors.Is(o.err, auth.ErrRefreshTokenReuse):
			reuseLosses++
		default:
			otherErrs++
			t.Errorf("unexpected error from concurrent Refresh: %v", o.err)
		}
	}

	if wins != 1 {
		t.Fatalf("wins = %d, want exactly 1 (got %d clean losses, %d reuse losses, %d other errors)", wins, cleanLosses, reuseLosses, otherErrs)
	}
	if reuseLosses != 0 {
		t.Fatalf("reuseLosses = %d, want 0: a concurrent race loser must get a clean 401, not family revocation (ErrRefreshTokenReuse)", reuseLosses)
	}
	if cleanLosses != attempts-1 {
		t.Fatalf("cleanLosses = %d, want %d", cleanLosses, attempts-1)
	}

	// Directly assert the family was not revoked: exactly one active
	// (non-revoked) refresh token must remain for this device — the
	// winner's new token — not zero (which family revocation would leave).
	var activeCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM refresh_tokens WHERE device_id = $1 AND revoked_at IS NULL`,
		registered.Device.ID,
	).Scan(&activeCount); err != nil {
		t.Fatalf("count active refresh tokens: %v", err)
	}
	if activeCount != 1 {
		t.Fatalf("active refresh token count for device = %d, want 1 (the winner's new token, family not revoked)", activeCount)
	}

	// The winner's new token must still be usable: rotate it once more.
	if _, err := svc.Refresh(ctx, winnerToken, nil); err != nil {
		t.Fatalf("winner's new refresh token should still be active, got: %v", err)
	}
}

// TestRefreshGenuineReuse_SequentialStaleTokenRevokesFamily asserts that
// RIZ-32 M2's fix did not break genuine reuse detection: presenting an
// already-rotated (now stale) token again in a later, non-concurrent
// request must still revoke the whole token family, per
// documentation/security.md's reuse-detection flow.
func TestRefreshGenuineReuse_SequentialStaleTokenRevokesFamily(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("reuser"), "correct-horse-battery-staple", testDevice("Reuser's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	staleToken := registered.Tokens.RefreshToken

	rotated, err := svc.Refresh(ctx, staleToken, nil)
	if err != nil {
		t.Fatalf("first Refresh (expected success): %v", err)
	}
	liveToken := rotated.Tokens.RefreshToken

	// Presenting the now-stale original token again, well after the first
	// rotation fully committed (no concurrency involved), must be detected
	// as genuine reuse and revoke the entire family.
	_, err = svc.Refresh(ctx, staleToken, nil)
	if !errors.Is(err, auth.ErrRefreshTokenReuse) {
		t.Fatalf("sequential reuse of a stale token: err = %v, want ErrRefreshTokenReuse", err)
	}

	// The legitimately rotated (and until-now-live) token must now also be
	// dead, since reuse detection revokes the whole family.
	if _, err := svc.Refresh(ctx, liveToken, nil); err == nil {
		t.Fatal("expected the live token to be revoked after reuse detection, but Refresh succeeded")
	}
}
