package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mirkru37/rize-backend/internal/auth"
)

// TestRevokeDeviceTxAgainstRealDatabase exercises Service.RevokeDevice's
// transactional path (revokeDeviceTx), which only runs when Service.Pool
// is a real *pgxpool.Pool (see RevokeDevice's dispatch and
// newDBBackedTestService, both in this file's sibling
// refresh_concurrency_test.go) — every other RevokeDevice test in this
// package uses the in-memory fakeQuerier, which never sets Pool and so
// only ever reaches the non-transactional fallback.
func TestRevokeDeviceTxAgainstRealDatabase(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("revoke-tx"), "correct-horse-battery-staple", testDevice("Revoke Tx's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := svc.RevokeDevice(ctx, registered.User.ID.String(), registered.Device.ID.String()); err != nil {
		t.Fatalf("RevokeDevice: %v", err)
	}

	devices, err := svc.ListDevices(ctx, registered.User.ID.String())
	if err != nil {
		t.Fatalf("ListDevices: %v", err)
	}
	for _, d := range devices {
		if d.ID == registered.Device.ID {
			t.Fatal("revoked device still appears in ListDevices")
		}
	}
}

// TestRevokeDeviceTxUnknownDeviceNotFound asserts revokeDeviceTx reports
// ErrDeviceNotFound (rather than a raw driver error) for a device id that
// does not exist or does not belong to the caller, mirroring
// revokeDeviceNoTx's behavior exercised by service_test.go's fakeQuerier
// tests.
func TestRevokeDeviceTxUnknownDeviceNotFound(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("revoke-tx-404"), "correct-horse-battery-staple", testDevice("404's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	err = svc.RevokeDevice(ctx, registered.User.ID.String(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("RevokeDevice (unknown device) = %v, want ErrDeviceNotFound", err)
	}
}

// TestRefreshTxSimpleSuccess exercises Service.refreshTx's plain,
// uncontended success path (no device metadata update), which every other
// Tx-path test in refresh_concurrency_test.go either races or tests reuse
// detection instead of this straightforward case.
func TestRefreshTxSimpleSuccess(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("refresh-tx-simple"), "correct-horse-battery-staple", testDevice("Simple Tx's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	refreshed, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if refreshed.Tokens.RefreshToken == registered.Tokens.RefreshToken {
		t.Error("Refresh did not rotate the refresh token")
	}
	if refreshed.User.ID != registered.User.ID {
		t.Error("Refresh resolved a different user")
	}

	// The old token must now be rejected.
	if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); err == nil {
		t.Error("expected the old, already-rotated refresh token to be rejected")
	}
}

// TestRefreshTxWithDeviceMetadataUpdate exercises refreshTx's
// device-metadata-update branch (device != nil), against a real
// transaction rather than the fakeQuerier path already covered by
// TestRefreshWithDeviceMetadataUpdatesDevice.
func TestRefreshTxWithDeviceMetadataUpdate(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("refresh-tx-device"), "correct-horse-battery-staple", testDevice("Old Tx Name"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	updatedDevice := testDevice("New Tx Name")
	refreshed, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, &updatedDevice)
	if err != nil {
		t.Fatalf("Refresh with device metadata: %v", err)
	}
	if refreshed.Device.Name != "New Tx Name" {
		t.Errorf("Device.Name = %q, want %q", refreshed.Device.Name, "New Tx Name")
	}
}

// TestRefreshTxExpiredToken exercises refreshTx's expired-token branch
// against a real transaction (TestRefreshExpired in service_test.go only
// covers the fakeQuerier/refreshNoTx path): the token's expires_at is
// backdated directly via SQL (RefreshTokenTTL is 30 days, too long to
// wait out in a test), then Refresh must reject it as
// ErrInvalidRefreshToken without rotating it.
func TestRefreshTxExpiredToken(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("refresh-tx-expired"), "correct-horse-battery-staple", testDevice("Expired Tx's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE refresh_tokens SET expires_at = now() - interval '1 hour'
		WHERE user_id = $1 AND revoked_at IS NULL`,
		registered.User.ID,
	); err != nil {
		t.Fatalf("backdate refresh token expiry: %v", err)
	}

	if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); !errors.Is(err, auth.ErrInvalidRefreshToken) {
		t.Fatalf("Refresh (expired, Tx path) = %v, want ErrInvalidRefreshToken", err)
	}
}

// TestUpdateProfileAgainstRealDatabase exercises Service.UpdateProfile
// against a real database, complementing service_test.go's fakeQuerier
// coverage of the same method.
func TestUpdateProfileAgainstRealDatabase(t *testing.T) {
	pool := testDBPool(t)
	svc := newDBBackedTestService(t, pool)
	ctx := context.Background()

	registered, err := svc.Register(ctx, uniqueEmail("update-profile"), "correct-horse-battery-staple", testDevice("Update Profile's MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	displayName := "Updated Name"
	timezone := "America/New_York"
	updated, err := svc.UpdateProfile(ctx, registered.User.ID.String(), auth.ProfileUpdate{
		DisplayName: &displayName,
		Timezone:    &timezone,
	})
	if err != nil {
		t.Fatalf("UpdateProfile: %v", err)
	}
	if updated.DisplayName == nil || *updated.DisplayName != displayName {
		t.Errorf("DisplayName = %v, want %q", updated.DisplayName, displayName)
	}

	fetched, err := svc.GetProfile(ctx, registered.User.ID.String())
	if err != nil {
		t.Fatalf("GetProfile: %v", err)
	}
	if fetched.DisplayName == nil || *fetched.DisplayName != displayName {
		t.Errorf("GetProfile after update DisplayName = %v, want %q", fetched.DisplayName, displayName)
	}
}
