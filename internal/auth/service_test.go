package auth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mirkru37/rize-backend/internal/auth"
)

func testSigningKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test RSA key: %v", err)
	}
	return key
}

// clock is a simple mutable clock injected into Service.Now, so
// expiry/rotation tests never need to sleep.
type clock struct{ now time.Time }

// newClock starts at the real wall-clock time rather than a fixed date,
// since access tokens are JWTs whose "exp" claim is validated by the JWT
// library against real time.Now() (VerifyAccessToken/jwt.ParseWithClaims
// take no injectable clock); tests that need to simulate the passage of
// time (e.g. refresh-token expiry) call Advance and only exercise
// Service-level logic that compares against its own injected clock, never
// JWT verification.
func newClock() *clock { return &clock{now: time.Now()} }

func (c *clock) Now() time.Time          { return c.now }
func (c *clock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func newTestService(t *testing.T) (*auth.Service, *clock) {
	t.Helper()
	c := newClock()
	return &auth.Service{
		Queries:    newFakeQuerier(),
		SigningKey: testSigningKey(t),
		Now:        c.Now,
	}, c
}

func testDevice(name string) auth.DeviceInput {
	return auth.DeviceInput{
		Platform:   "macos",
		Name:       name,
		Model:      "MacBookPro18,1",
		OSVersion:  "14.5",
		AppVersion: "1.0.0",
	}
}

func uniqueEmail(prefix string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%s+%x@example.com", prefix, b)
}

// TestRegisterLoginRefreshMeHappyPath walks the full
// register -> login -> refresh -> GetProfile path a real client would use.
func TestRegisterLoginRefreshMeHappyPath(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("alice")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Alice's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if registered.Tokens.AccessToken == "" || registered.Tokens.RefreshToken == "" {
		t.Fatal("Register did not return both tokens")
	}
	if registered.User.Email == nil || *registered.User.Email != email {
		t.Errorf("registered user email = %v, want %q", registered.User.Email, email)
	}

	loggedIn, err := svc.Login(ctx, email, "correct-horse-battery-staple", testDevice("Alice's MacBook"))
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if loggedIn.User.ID != registered.User.ID {
		t.Errorf("Login resolved a different user than Register created")
	}

	refreshed, err := svc.Refresh(ctx, loggedIn.Tokens.RefreshToken, nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.Tokens.RefreshToken == loggedIn.Tokens.RefreshToken {
		t.Error("Refresh returned the same refresh token instead of rotating it")
	}

	profile, err := svc.GetProfile(ctx, refreshed.User.ID.String())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if profile.ID != registered.User.ID {
		t.Errorf("GetProfile returned a different user")
	}
}

// TestRefreshRotation_OldTokenInvalidAfterRotation asserts that once a
// refresh token has been used, it cannot be used again (except to trigger
// reuse detection), per documentation/security.md's rotation flow.
func TestRefreshRotation_OldTokenInvalidAfterRotation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("bob")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Bob's iPhone"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	oldToken := registered.Tokens.RefreshToken

	if _, err := svc.Refresh(ctx, oldToken, nil); err != nil {
		t.Fatalf("first Refresh (expected success): %v", err)
	}

	// Presenting the same (now-rotated) token again must fail...
	_, err = svc.Refresh(ctx, oldToken, nil)
	if err == nil {
		t.Fatal("second Refresh with the same old token succeeded, want an error")
	}
	if !errors.Is(err, auth.ErrRefreshTokenReuse) {
		t.Errorf("second Refresh error = %v, want ErrRefreshTokenReuse", err)
	}
}

// TestRefreshReuseDetection_RevokesEntireFamily asserts that reuse of an
// already-rotated token revokes every token in its family (documented as
// the mechanism that bounds the blast radius of a leaked refresh token).
func TestRefreshReuseDetection_RevokesEntireFamily(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("carol")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Carol's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	oldToken := registered.Tokens.RefreshToken

	rotated, err := svc.Refresh(ctx, oldToken, nil)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	newToken := rotated.Tokens.RefreshToken

	// Reuse the already-rotated old token: this must revoke the family,
	// including the freshly rotated newToken.
	if _, err := svc.Refresh(ctx, oldToken, nil); !errors.Is(err, auth.ErrRefreshTokenReuse) {
		t.Fatalf("expected ErrRefreshTokenReuse, got %v", err)
	}

	// The legitimately rotated token must now also be dead, because reuse
	// detection revokes the whole family.
	if _, err := svc.Refresh(ctx, newToken, nil); err == nil {
		t.Fatal("expected the newly-rotated token to be revoked after reuse detection, but Refresh succeeded")
	}
}

// TestRefreshExpired asserts that a refresh token past its expiry is
// rejected, using an injected clock rather than a real 30-day sleep.
func TestRefreshExpired(t *testing.T) {
	svc, clk := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("dave")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Dave's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	clk.Advance(auth.RefreshTokenTTL + time.Minute)

	_, err = svc.Refresh(ctx, registered.Tokens.RefreshToken, nil)
	if !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Fatalf("Refresh after expiry = %v, want ErrInvalidRefreshToken", err)
	}
}

// TestLoginWrongPassword asserts a wrong password is rejected with
// ErrInvalidCredentials (mapped to 401 by the handler), per
// documentation/api-reference.md's worked login example.
func TestLoginWrongPassword(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("erin")

	if _, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Erin's MacBook")); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := svc.Login(ctx, email, "wrong-password", testDevice("Erin's MacBook"))
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login with wrong password = %v, want ErrInvalidCredentials", err)
	}
}

// TestLoginUnknownEmail asserts an unknown email is rejected identically to
// a wrong password (no account-enumeration signal), per
// documentation/security.md.
func TestLoginUnknownEmail(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	_, err := svc.Login(ctx, uniqueEmail("nobody"), "whatever12345", testDevice("Nobody's MacBook"))
	if !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("Login with unknown email = %v, want ErrInvalidCredentials", err)
	}
}

// TestRegisterDuplicateEmail asserts a second registration with the same
// email is rejected.
func TestRegisterDuplicateEmail(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("frank")

	if _, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Frank's MacBook")); err != nil {
		t.Fatalf("first Register: %v", err)
	}

	_, err := svc.Register(ctx, email, "another-password-1234", testDevice("Frank's iPhone"))
	if !errors.Is(err, auth.ErrEmailTaken) {
		t.Fatalf("second Register with same email = %v, want ErrEmailTaken", err)
	}
}

// TestLogoutRevocation asserts logout revokes the refresh token so it can no
// longer be used to refresh.
func TestLogoutRevocation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("grace")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Grace's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := svc.Logout(ctx, registered.User.ID.String(), registered.Tokens.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); err == nil {
		t.Fatal("Refresh after Logout succeeded, want an error")
	}
}

// TestLogoutTenantIsolation asserts one user cannot log out (revoke) another
// user's refresh token by presenting it alongside their own user id.
func TestLogoutTenantIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	victim, err := svc.Register(ctx, uniqueEmail("victim"), "correct-horse-battery-staple", testDevice("Victim's MacBook"))
	if err != nil {
		t.Fatalf("Register victim: %v", err)
	}
	attacker, err := svc.Register(ctx, uniqueEmail("attacker"), "correct-horse-battery-staple", testDevice("Attacker's MacBook"))
	if err != nil {
		t.Fatalf("Register attacker: %v", err)
	}

	err = svc.Logout(ctx, attacker.User.ID.String(), victim.Tokens.RefreshToken)
	if !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Fatalf("cross-tenant Logout = %v, want ErrInvalidRefreshToken", err)
	}

	// The victim's token must still work after the attacker's attempt.
	if _, err := svc.Refresh(ctx, victim.Tokens.RefreshToken, nil); err != nil {
		t.Fatalf("victim's Refresh after cross-tenant logout attempt: %v", err)
	}
}

// TestDeviceTenantIsolation asserts one user cannot rename, list, or revoke
// another user's device, per documentation/security.md §Tenant Isolation.
func TestDeviceTenantIsolation(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	owner, err := svc.Register(ctx, uniqueEmail("owner"), "correct-horse-battery-staple", testDevice("Owner's MacBook"))
	if err != nil {
		t.Fatalf("Register owner: %v", err)
	}
	other, err := svc.Register(ctx, uniqueEmail("other"), "correct-horse-battery-staple", testDevice("Other's MacBook"))
	if err != nil {
		t.Fatalf("Register other: %v", err)
	}

	// other cannot rename owner's device.
	if _, err := svc.RenameDevice(ctx, other.User.ID.String(), owner.Device.ID.String(), "hijacked"); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("cross-tenant RenameDevice = %v, want ErrDeviceNotFound", err)
	}

	// other cannot revoke owner's device.
	if err := svc.RevokeDevice(ctx, other.User.ID.String(), owner.Device.ID.String()); !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("cross-tenant RevokeDevice = %v, want ErrDeviceNotFound", err)
	}

	// other's device list must not include owner's device.
	devices, err := svc.ListDevices(ctx, other.User.ID.String())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	for _, d := range devices {
		if d.ID == owner.Device.ID {
			t.Fatal("other's ListDevices leaked owner's device")
		}
	}

	// owner's device must still be usable.
	if _, err := svc.RenameDevice(ctx, owner.User.ID.String(), owner.Device.ID.String(), "still mine"); err != nil {
		t.Fatalf("owner's RenameDevice: %v", err)
	}
}

// TestLoginTwiceSameDevice_ExactlyOneActiveRefreshTokenPerDevice asserts
// RIZ-32 H1's fix: documentation/security.md's token model table requires
// "exactly one active refresh token per device". Logging in twice while
// echoing back the same device.id (as a real client reconnecting on the
// same device would) must revoke the first login's refresh token family,
// leaving only the second login's token usable — and after logging out of
// that second session, the device must have no active refresh token at
// all.
func TestLoginTwiceSameDevice_ExactlyOneActiveRefreshTokenPerDevice(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	email := uniqueEmail("ivan")

	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Ivan's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	firstLoginToken := registered.Tokens.RefreshToken

	// Log in again, echoing back the same device id (as documented for
	// DeviceInput.ID: a client reconnecting to its existing device row).
	reconnectDevice := testDevice("Ivan's MacBook")
	reconnectDevice.ID = registered.Device.ID.String()

	secondLogin, err := svc.Login(ctx, email, "correct-horse-battery-staple", reconnectDevice)
	if err != nil {
		t.Fatalf("second Login: %v", err)
	}
	if secondLogin.Device.ID != registered.Device.ID {
		t.Fatalf("second Login resolved a different device (got %v, want %v)", secondLogin.Device.ID, registered.Device.ID)
	}
	secondLoginToken := secondLogin.Tokens.RefreshToken

	// The first login's refresh token must now be dead: it was minted for
	// the same device, and issuing the second login's token pair must have
	// revoked it.
	if _, err := svc.Refresh(ctx, firstLoginToken, nil); err == nil {
		t.Fatal("first login's refresh token still refreshes after a second login on the same device, want an error")
	}

	// The second login's token must still work.
	refreshed, err := svc.Refresh(ctx, secondLoginToken, nil)
	if err != nil {
		t.Fatalf("second login's refresh token should still be active: %v", err)
	}

	// Logging out of the (now-rotated) second session must leave the
	// device with no active refresh token at all.
	if err := svc.Logout(ctx, refreshed.User.ID.String(), refreshed.Tokens.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	active, err := svc.Queries.ListActiveRefreshTokensByUser(ctx, refreshed.User.ID)
	if err != nil {
		t.Fatalf("ListActiveRefreshTokensByUser: %v", err)
	}
	for _, rt := range active {
		if rt.DeviceID == registered.Device.ID {
			t.Fatalf("device %v still has an active refresh token after double-login + logout: %+v", registered.Device.ID, rt)
		}
	}
}

// TestRevokeDeviceRevokesRefreshTokens asserts DELETE /v1/devices/{id}'s
// service method revokes every refresh token issued to that device, per
// documentation/api-reference.md §Devices.
func TestRevokeDeviceRevokesRefreshTokens(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("hank"), "correct-horse-battery-staple", testDevice("Hank's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := svc.RevokeDevice(ctx, registered.User.ID.String(), registered.Device.ID.String()); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); err == nil {
		t.Fatal("Refresh succeeded using a token bound to a revoked device")
	}
}
