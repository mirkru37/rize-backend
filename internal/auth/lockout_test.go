package auth_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
)

// newLockoutTestService is newTestService with configurable lockout
// parameters, so tests can use a small threshold/duration instead of the
// production defaults (10 attempts / 15m-24h) and stay fast and readable.
func newLockoutTestService(t *testing.T, threshold int, base, maxDur time.Duration) (*auth.Service, *clock) {
	t.Helper()
	c := newClock()
	return &auth.Service{
		Queries:             newFakeQuerier(),
		SigningKey:          testSigningKey(t),
		Now:                 c.Now,
		LockoutThreshold:    threshold,
		LockoutBaseDuration: base,
		LockoutMaxDuration:  maxDur,
	}, c
}

// newLockoutTestRouter mirrors handlers_test.go's newTestRouter, but wired
// to a caller-supplied Service so lockout tests can exercise the full HTTP
// stack (RFC 7807 envelope, status code) with a small, fast threshold.
func newLockoutTestRouter(svc *auth.Service) *chi.Mux {
	handler := auth.NewHandler(svc)
	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		auth.RegisterRoutes(r, handler,
			appmw.Authenticate(&svc.SigningKey.PublicKey),
			appmw.RequireRole("admin"),
		)
	})
	return r
}

const lockoutTestPassword = "correct-horse-battery-staple"

// registerLockoutTestUser registers a fresh account for lockout tests and
// returns its email, so callers can drive repeated Login attempts against
// it.
func registerLockoutTestUser(t *testing.T, svc *auth.Service, prefix string) string {
	t.Helper()
	email := uniqueEmail(prefix)
	if _, err := svc.Register(context.Background(), email, lockoutTestPassword, testDevice(prefix+"'s MacBook")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	return email
}

// failLoginNTimes calls Login with a wrong password n times, failing the
// test if any of those calls does not return ErrInvalidCredentials.
func failLoginNTimes(t *testing.T, svc *auth.Service, email string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		_, err := svc.Login(context.Background(), email, "wrong-password", testDevice("attempt"))
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("Login attempt %d: err = %v, want ErrInvalidCredentials", i+1, err)
		}
	}
}

// TestLogin_LockoutTriggersAtThreshold asserts RIZ-59: the Nth consecutive
// failed login (N == threshold) locks the account, and the very next
// login attempt — even with the CORRECT password — is rejected while
// locked.
func TestLogin_LockoutTriggersAtThreshold(t *testing.T) {
	tests := []struct {
		name      string
		threshold int
	}{
		{name: "threshold 1", threshold: 1},
		{name: "threshold 3", threshold: 3},
		{name: "threshold 10 (production default)", threshold: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newLockoutTestService(t, tt.threshold, 15*time.Minute, 24*time.Hour)
			email := registerLockoutTestUser(t, svc, "lockout")

			failLoginNTimes(t, svc, email, tt.threshold)

			// Locked now: even the correct password must be rejected.
			_, err := svc.Login(context.Background(), email, lockoutTestPassword, testDevice("post-lock"))
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("Login with correct password while locked: err = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

// TestLogin_LockedRejectedIdenticallyToBadPassword asserts RIZ-59's
// no-oracle requirement at the HTTP layer: a login against a locked
// account and a login with a plain wrong password against an unlocked
// account produce the exact same status code and RFC 7807 response body
// shape (type/title/detail), so a client (or attacker) cannot distinguish
// "locked" from "wrong password" by inspecting the response.
func TestLogin_LockedRejectedIdenticallyToBadPassword(t *testing.T) {
	const threshold = 3
	svc, _ := newLockoutTestService(t, threshold, 15*time.Minute, 24*time.Hour)
	r := newLockoutTestRouter(svc)

	lockedEmail := registerLockoutTestUser(t, svc, "locked")
	failLoginNTimes(t, svc, lockedEmail, threshold)
	lockedRec := doJSON(t, r, http.MethodPost, "/v1/auth/login", map[string]any{
		"email":    lockedEmail,
		"password": lockoutTestPassword, // correct, but the account is locked
		"device":   registerDeviceBody("Locked's MacBook"),
	}, "")

	badPasswordEmail := registerLockoutTestUser(t, svc, "badpw")
	badPasswordRec := doJSON(t, r, http.MethodPost, "/v1/auth/login", map[string]any{
		"email":    badPasswordEmail,
		"password": "wrong-password",
		"device":   registerDeviceBody("BadPW's MacBook"),
	}, "")

	if lockedRec.Code != http.StatusUnauthorized {
		t.Fatalf("locked login status = %d, want 401", lockedRec.Code)
	}
	if lockedRec.Code != badPasswordRec.Code {
		t.Fatalf("locked status = %d, bad-password status = %d, want equal", lockedRec.Code, badPasswordRec.Code)
	}

	var lockedBody, badPasswordBody map[string]any
	if err := json.Unmarshal(lockedRec.Body.Bytes(), &lockedBody); err != nil {
		t.Fatalf("decode locked body: %v", err)
	}
	if err := json.Unmarshal(badPasswordRec.Body.Bytes(), &badPasswordBody); err != nil {
		t.Fatalf("decode bad-password body: %v", err)
	}

	for _, field := range []string{"type", "title", "status", "detail"} {
		if lockedBody[field] != badPasswordBody[field] {
			t.Errorf("response field %q differs: locked = %v, bad-password = %v (this would be a lockout oracle)", field, lockedBody[field], badPasswordBody[field])
		}
	}
}

// TestLogin_CooldownExpiryUnlocksAccount asserts that once the injected
// clock advances past locked_until, a correct login succeeds again without
// any wall-clock sleep.
func TestLogin_CooldownExpiryUnlocksAccount(t *testing.T) {
	const threshold = 2
	base := 10 * time.Minute
	svc, clk := newLockoutTestService(t, threshold, base, 24*time.Hour)
	email := registerLockoutTestUser(t, svc, "cooldown")

	failLoginNTimes(t, svc, email, threshold)

	// Still locked immediately after.
	if _, err := svc.Login(context.Background(), email, lockoutTestPassword, testDevice("still-locked")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login immediately after lockout: err = %v, want ErrInvalidCredentials", err)
	}

	// Not quite expired yet.
	clk.Advance(base - time.Second)
	if _, err := svc.Login(context.Background(), email, lockoutTestPassword, testDevice("not-yet")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login 1s before cooldown expiry: err = %v, want ErrInvalidCredentials", err)
	}

	// Past expiry: the correct password now succeeds.
	clk.Advance(2 * time.Second)
	result, err := svc.Login(context.Background(), email, lockoutTestPassword, testDevice("unlocked"))
	if err != nil {
		t.Fatalf("Login after cooldown expiry: %v", err)
	}
	if result.User.LockedUntil.Valid {
		t.Errorf("LockedUntil still set after a successful post-expiry login")
	}
}

// TestLogin_EscalationDoubling asserts that each subsequent lockout on the
// same account doubles the previous lockout duration, capped at
// LockoutMaxDuration.
func TestLogin_EscalationDoubling(t *testing.T) {
	const threshold = 2
	base := 1 * time.Minute
	maxDur := 5 * time.Minute // caps the 3rd doubling (1m, 2m, 4m, 8m->capped to 5m)
	svc, clk := newLockoutTestService(t, threshold, base, maxDur)
	email := registerLockoutTestUser(t, svc, "escalate")

	wantDurations := []time.Duration{
		1 * time.Minute, // 1st lockout: base * 2^0
		2 * time.Minute, // 2nd lockout: base * 2^1
		4 * time.Minute, // 3rd lockout: base * 2^2
		5 * time.Minute, // 4th lockout: base * 2^3 = 8m, capped at max (5m)
	}

	for i, want := range wantDurations {
		// Trigger a lockout: fail 'threshold' times. If a previous
		// lockout is still active this first failed attempt is rejected
		// without touching counters/lock state (per the no-extension
		// design), so advance past it first.
		beforeLock := clk.Now()
		failLoginNTimes(t, svc, email, threshold)

		result, err := svc.Login(context.Background(), email, "wrong-password-to-read-state", testDevice("probe"))
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Fatalf("lockout %d: probe login err = %v, want ErrInvalidCredentials", i+1, err)
		}
		_ = result // Login on failure doesn't return a Result; state is read below instead.

		locked := lockedUntil(t, svc, email)
		got := locked.Sub(beforeLock)
		if got != want {
			t.Errorf("lockout %d duration = %v, want %v", i+1, got, want)
		}

		// Advance past this lockout's expiry before the next iteration.
		clk.Advance(want + time.Second)
	}
}

// lockedUntil reads the account's current locked_until via a locked login
// attempt's side channel: since Login doesn't expose the User row on
// failure, this uses ResetLoginLockout's counterpart path instead — a
// direct Queries call through the exported Service.Queries field, which
// package auth_test has access to since Service embeds storedb.Querier
// directly.
func lockedUntil(t *testing.T, svc *auth.Service, email string) time.Time {
	t.Helper()
	user, err := svc.Queries.GetUserByEmail(context.Background(), &email)
	if err != nil {
		t.Fatalf("GetUserByEmail: %v", err)
	}
	if !user.LockedUntil.Valid {
		t.Fatalf("account %q is not locked", email)
	}
	return user.LockedUntil.Time
}

// TestLogin_ResetOnSuccessfulLogin asserts that a successful login clears
// both the failed-attempt counter and the lockout-escalation counter, per
// documentation/security.md's reset semantics.
func TestLogin_ResetOnSuccessfulLogin(t *testing.T) {
	const threshold = 5
	svc, clk := newLockoutTestService(t, threshold, time.Minute, time.Hour)
	email := registerLockoutTestUser(t, svc, "reset")

	// Fail a few times, but stay under the threshold.
	failLoginNTimes(t, svc, email, threshold-2)

	result, err := svc.Login(context.Background(), email, lockoutTestPassword, testDevice("success"))
	if err != nil {
		t.Fatalf("Login with correct password: %v", err)
	}
	if result.User.FailedLoginAttempts != 0 {
		t.Errorf("FailedLoginAttempts after successful login = %d, want 0", result.User.FailedLoginAttempts)
	}
	if result.User.LockoutCount != 0 {
		t.Errorf("LockoutCount after successful login = %d, want 0", result.User.LockoutCount)
	}
	if result.User.LockedUntil.Valid {
		t.Errorf("LockedUntil after successful login is set, want unset")
	}

	// Escalation counter must also reset: triggering a fresh lockout after
	// a successful login must use the BASE duration again, not a doubled
	// one.
	beforeLock := clk.Now()
	failLoginNTimes(t, svc, email, threshold)
	locked := lockedUntil(t, svc, email)
	if got, want := locked.Sub(beforeLock), time.Minute; got != want {
		t.Errorf("post-reset lockout duration = %v, want %v (base, not escalated)", got, want)
	}
}

// TestLogin_PerAccountIsolation asserts that locking account A does not
// affect account B: B can still log in normally while A is locked out.
func TestLogin_PerAccountIsolation(t *testing.T) {
	const threshold = 3
	svc, _ := newLockoutTestService(t, threshold, 15*time.Minute, 24*time.Hour)

	emailA := registerLockoutTestUser(t, svc, "isolation-a")
	emailB := registerLockoutTestUser(t, svc, "isolation-b")

	failLoginNTimes(t, svc, emailA, threshold)

	// A is locked.
	if _, err := svc.Login(context.Background(), emailA, lockoutTestPassword, testDevice("a-locked")); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login(A) while locked: err = %v, want ErrInvalidCredentials", err)
	}

	// B is entirely unaffected.
	result, err := svc.Login(context.Background(), emailB, lockoutTestPassword, testDevice("b-fine"))
	if err != nil {
		t.Fatalf("Login(B) = %v, want success (B must not be affected by A's lockout)", err)
	}
	if result.User.LockedUntil.Valid {
		t.Errorf("B's LockedUntil is set; A's lockout leaked into B's account state")
	}
}

// TestLogin_NoOracleAcrossFailureModes asserts that every Login failure
// mode covered by RIZ-59 (locked account, wrong password, unknown email)
// produces the identical ErrInvalidCredentials sentinel — the single
// choke point writeServiceError maps to one RFC 7807 response — so no
// failure mode is distinguishable from any other at the Service layer
// either, not just at the HTTP layer (see
// TestLogin_LockedRejectedIdenticallyToBadPassword for the HTTP-level
// assertion).
func TestLogin_NoOracleAcrossFailureModes(t *testing.T) {
	const threshold = 2
	svc, _ := newLockoutTestService(t, threshold, 15*time.Minute, 24*time.Hour)

	lockedEmail := registerLockoutTestUser(t, svc, "oracle-locked")
	failLoginNTimes(t, svc, lockedEmail, threshold)

	badPasswordEmail := registerLockoutTestUser(t, svc, "oracle-badpw")

	cases := []struct {
		name  string
		email string
		pass  string
	}{
		{name: "locked account, correct password", email: lockedEmail, pass: lockoutTestPassword},
		{name: "wrong password", email: badPasswordEmail, pass: "wrong-password"},
		{name: "unknown email", email: uniqueEmail("oracle-unknown"), pass: "whatever-password"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Login(context.Background(), tc.email, tc.pass, testDevice("oracle-probe"))
			if !errors.Is(err, auth.ErrInvalidCredentials) {
				t.Fatalf("Login(%s) = %v, want ErrInvalidCredentials", tc.name, err)
			}
		})
	}
}
