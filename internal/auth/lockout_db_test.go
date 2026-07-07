package auth_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// newDBBackedLockoutTestService mirrors refresh_concurrency_test.go's
// newDBBackedTestService, with a small, fast lockout threshold/duration so
// this test doesn't need the 10-attempt production default.
func newDBBackedLockoutTestService(t *testing.T, pool storedb.DBTX, threshold int, base, maxDur time.Duration) *auth.Service {
	t.Helper()
	return &auth.Service{
		Queries:             storedb.New(pool),
		SigningKey:          testSigningKey(t),
		LockoutThreshold:    threshold,
		LockoutBaseDuration: base,
		LockoutMaxDuration:  maxDur,
	}
}

// TestLogin_ConcurrentFailedAttempts_NoLostUpdates exercises RIZ-59's
// race-safety requirement against a real database: many goroutines fail a
// login against the very same account concurrently. Because
// RecordFailedLoginAttempt is a single atomic UPDATE (see
// login_lockout.sql), Postgres's row-level write lock serializes every
// concurrent caller — a read-modify-write implementation would instead
// lose updates under this exact contention pattern, undercounting
// failed_login_attempts.
func TestLogin_ConcurrentFailedAttempts_NoLostUpdates(t *testing.T) {
	pool := testDBPool(t)
	const threshold = 50 // high enough that no goroutine actually triggers a lockout mid-race
	svc := newDBBackedLockoutTestService(t, pool, threshold, 15*time.Minute, 24*time.Hour)
	ctx := context.Background()

	email := uniqueEmail("concurrent-lockout")
	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Concurrent's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	const attempts = 20
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Login(ctx, email, "wrong-password", testDevice("racer"))
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Errorf("concurrent Login: err = %v, want ErrInvalidCredentials", err)
			}
		}()
	}
	wg.Wait()

	user, err := svc.Queries.GetUserByID(ctx, registered.User.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if int(user.FailedLoginAttempts) != attempts {
		t.Errorf("FailedLoginAttempts = %d, want %d (no lost updates under concurrent failed logins)", user.FailedLoginAttempts, attempts)
	}
}

// TestLogin_ConcurrentFailedAttempts_LockoutEscalatesExactlyOnce asserts
// RIZ-59's post-review fix (MEDIUM finding): a 20-way concurrent burst of
// failed logins against the same account — well past the lockout
// threshold — must escalate lockout_count exactly ONCE per lockout
// episode, not once per attempt that independently satisfies
// "failed_login_attempts + 1 >= threshold" as the count climbs past it.
// login_lockout.sql's RecordFailedLoginAttempt now additionally guards
// both the lockout_count increment and the locked_until assignment on
// "not currently locked" (locked_until IS NULL OR locked_until <= @now),
// so once the row is locked, every other concurrent attempt in the same
// burst (whose own @now is still well before locked_until) still
// increments failed_login_attempts but does not re-escalate. Before this
// fix, a burst like this could inflate lockout_count by roughly
// (attempts - threshold + 1) in one go, which is what fed the HIGH
// interval-overflow finding this PR also fixes (see
// TestRecordFailedLoginAttempt_LockoutCountAboveOverflowThreshold).
func TestLogin_ConcurrentFailedAttempts_LockoutEscalatesExactlyOnce(t *testing.T) {
	pool := testDBPool(t)
	const threshold = 5
	svc := newDBBackedLockoutTestService(t, pool, threshold, 15*time.Minute, 24*time.Hour)
	ctx := context.Background()

	email := uniqueEmail("concurrent-escalation")
	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Escalation's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	const attempts = 20 // well past threshold
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = svc.Login(ctx, email, "wrong-password", testDevice("racer"))
		}()
	}
	wg.Wait()

	user, err := svc.Queries.GetUserByID(ctx, registered.User.ID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if int(user.FailedLoginAttempts) != attempts {
		t.Fatalf("FailedLoginAttempts = %d, want %d (no lost updates)", user.FailedLoginAttempts, attempts)
	}
	if user.LockoutCount != 1 {
		t.Errorf("LockoutCount = %d, want exactly 1 (one escalation per lockout episode, not once per attempt past the threshold)", user.LockoutCount)
	}
	if !user.LockedUntil.Valid {
		t.Errorf("LockedUntil not set after crossing the threshold")
	}
}

// TestRecordFailedLoginAttempt_LockoutCountAboveOverflowThreshold is a
// regression test for RIZ-59's post-review HIGH finding: doubling
// base_duration_seconds by 2^lockout_count and converting straight to an
// interval BEFORE capping at max_duration_seconds overflows Postgres's
// interval range once lockout_count is large enough (observed at
// lockout_count >= 34; 15m * 2^34 is on the order of hundreds of thousands
// of years). That overflow made RecordFailedLoginAttempt return a
// database error instead of a User row, which Login would have surfaced
// as a 500 Internal Server Error instead of the standard 401
// invalid-credentials envelope — itself a distinguishable signal (a
// lockout oracle) on top of just being a bug. login_lockout.sql now caps
// in the seconds (float8) domain before ever converting to an interval,
// so this must succeed and simply return the account capped at
// max_duration_seconds, no matter how large lockout_count has climbed.
func TestRecordFailedLoginAttempt_LockoutCountAboveOverflowThreshold(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedLockoutTestService(t, pool, 10, 15*time.Minute, 24*time.Hour)
	ctx := context.Background()

	email := uniqueEmail("overflow-guard")
	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Overflow's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Force the row into a state that previously reproduced the overflow:
	// lockout_count far above the ~34 threshold where base*2^lockout_count
	// exceeds Postgres's interval range, with failed_login_attempts one
	// short of the threshold so the next RecordFailedLoginAttempt call
	// both increments across the threshold AND is not already locked (so
	// the escalation branch actually runs).
	const priorLockoutCount = 40
	if _, err := pool.Exec(ctx,
		`UPDATE users SET failed_login_attempts = 9, lockout_count = $2, locked_until = NULL WHERE id = $1`,
		registered.User.ID, priorLockoutCount,
	); err != nil {
		t.Fatalf("seed lockout_count: %v", err)
	}

	now := time.Now()
	user, err := svc.Queries.RecordFailedLoginAttempt(ctx, storedb.RecordFailedLoginAttemptParams{
		ID:                  registered.User.ID,
		Threshold:           10,
		Now:                 pgtype.Timestamptz{Time: now, Valid: true},
		BaseDurationSeconds: (15 * time.Minute).Seconds(),
		MaxDurationSeconds:  (24 * time.Hour).Seconds(),
	})
	if err != nil {
		t.Fatalf("RecordFailedLoginAttempt with lockout_count=%d: %v (this must not error — see the HIGH finding this test guards against)", priorLockoutCount, err)
	}

	if int(user.LockoutCount) != priorLockoutCount+1 {
		t.Errorf("LockoutCount = %d, want %d", user.LockoutCount, priorLockoutCount+1)
	}
	if !user.LockedUntil.Valid {
		t.Fatalf("LockedUntil not set")
	}
	gotDuration := user.LockedUntil.Time.Sub(now)
	wantDuration := 24 * time.Hour
	// Allow a small tolerance for the round trip through Postgres/pgx.
	if diff := gotDuration - wantDuration; diff < -time.Second || diff > time.Second {
		t.Errorf("locked_until - now = %v, want capped at %v", gotDuration, wantDuration)
	}
}
