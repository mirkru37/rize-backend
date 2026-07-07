package auth_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/mirkru37/rize-backend/internal/auth"
)

// errInjectedDBFailure is a stand-in for an arbitrary, non-pgx.ErrNoRows
// database driver error (a dropped connection, a deadline, etc.), used to
// exercise Service methods' "unexpected database error" defensive
// branches — the fakeQuerier-backed tests elsewhere in this package only
// ever model the expected "row not found" outcome via pgx.ErrNoRows.
var errInjectedDBFailure = errors.New("injected: simulated database failure")

func TestGetProfileWrapsUnexpectedDatabaseError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	registered, err := svc.Register(ctx, uniqueEmail("getprofile-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	fq.failNextCallTo("GetUserByID", errInjectedDBFailure)
	if _, err := svc.GetProfile(ctx, registered.User.ID.String()); err == nil || errors.Is(err, auth.ErrUserNotFound) {
		t.Fatalf("GetProfile with an injected DB failure = %v, want a wrapped unexpected error (not ErrUserNotFound)", err)
	}
}

func TestListDevicesWrapsUnexpectedDatabaseError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	registered, err := svc.Register(ctx, uniqueEmail("listdevices-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	fq.failNextCallTo("ListDevicesByUser", errInjectedDBFailure)
	if _, err := svc.ListDevices(ctx, registered.User.ID.String()); err == nil {
		t.Fatal("ListDevices with an injected DB failure = nil, want an error")
	}
}

func TestRenameDeviceWrapsUnexpectedDatabaseError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	registered, err := svc.Register(ctx, uniqueEmail("renamedevice-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	fq.failNextCallTo("UpdateDeviceName", errInjectedDBFailure)
	if _, err := svc.RenameDevice(ctx, registered.User.ID.String(), registered.Device.ID.String(), "New Name"); err == nil || errors.Is(err, auth.ErrDeviceNotFound) {
		t.Fatalf("RenameDevice with an injected DB failure = %v, want a wrapped unexpected error (not ErrDeviceNotFound)", err)
	}
}

func TestUpdateProfileWrapsUnexpectedDatabaseError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	registered, err := svc.Register(ctx, uniqueEmail("updateprofile-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	name := "New Name"
	fq.failNextCallTo("UpdateUserProfile", errInjectedDBFailure)
	if _, err := svc.UpdateProfile(ctx, registered.User.ID.String(), auth.ProfileUpdate{DisplayName: &name}); err == nil {
		t.Fatal("UpdateProfile with an injected DB failure = nil, want an error")
	}
}

// TestRevokeDeviceNoTxWrapsUnexpectedDatabaseErrors is table-driven
// coverage for revokeDeviceNoTx's three sequential DB calls (get, revoke
// device, revoke refresh tokens), each of which must surface an
// unexpected failure as a wrapped error rather than swallowing it.
func TestRevokeDeviceNoTxWrapsUnexpectedDatabaseErrors(t *testing.T) {
	tests := []string{"GetDeviceByID", "RevokeDevice", "RevokeRefreshTokensByDevice"}

	for _, method := range tests {
		t.Run(method, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			fq := svc.Queries.(*fakeQuerier)

			registered, err := svc.Register(ctx, uniqueEmail("revokedevice-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			fq.failNextCallTo(method, errInjectedDBFailure)
			err = svc.RevokeDevice(ctx, registered.User.ID.String(), registered.Device.ID.String())
			if err == nil {
				t.Fatalf("RevokeDevice with an injected %s failure = nil, want an error", method)
			}
		})
	}
}

func TestRegisterWrapsUnexpectedDatabaseErrors(t *testing.T) {
	tests := []string{"CreateDevice", "CreateRefreshToken"}

	for _, method := range tests {
		t.Run(method, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			fq := svc.Queries.(*fakeQuerier)

			fq.failNextCallTo(method, errInjectedDBFailure)
			if _, err := svc.Register(ctx, uniqueEmail("register-dberr"), "correct-horse-battery-staple", testDevice("MacBook")); err == nil {
				t.Fatalf("Register with an injected %s failure = nil, want an error", method)
			}
		})
	}
}

// TestLoginWrapsResolveDeviceUpdateError exercises resolveDevice's own
// UpdateDeviceMetadata-failure branch (the "reconnect to an existing
// device by id" path), distinct from refreshNoTx's/refreshTx's use of the
// same method for a different purpose.
func TestLoginWrapsResolveDeviceUpdateError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	email := uniqueEmail("resolvedevice-dberr")
	registered, err := svc.Register(ctx, email, "correct-horse-battery-staple", testDevice("Original Name"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	reconnectDevice := testDevice("Reconnect Attempt")
	reconnectDevice.ID = registered.Device.ID.String()

	fq.failNextCallTo("UpdateDeviceMetadata", errInjectedDBFailure)
	if _, err := svc.Login(ctx, email, "correct-horse-battery-staple", reconnectDevice); err == nil {
		t.Fatal("Login with an injected UpdateDeviceMetadata failure = nil, want an error")
	}
}

// TestRegisterWrapsIssueTokenPairRevokeError exercises issueTokenPair's
// own RevokeRefreshTokensByDevice-failure branch (distinct from
// revokeDeviceNoTx's use of the same method, covered by
// TestRevokeDeviceNoTxWrapsUnexpectedDatabaseErrors).
func TestRegisterWrapsIssueTokenPairRevokeError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	fq.failNextCallTo("RevokeRefreshTokensByDevice", errInjectedDBFailure)
	if _, err := svc.Register(ctx, uniqueEmail("issuetokenpair-dberr"), "correct-horse-battery-staple", testDevice("MacBook")); err == nil {
		t.Fatal("Register with an injected RevokeRefreshTokensByDevice failure = nil, want an error")
	}
}

func TestRefreshNoTxWrapsUnexpectedDatabaseErrors(t *testing.T) {
	tests := []string{"GetUserByID", "GetDeviceByID", "CreateRefreshToken"}

	for _, method := range tests {
		t.Run(method, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			fq := svc.Queries.(*fakeQuerier)

			registered, err := svc.Register(ctx, uniqueEmail("refresh-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			fq.failNextCallTo(method, errInjectedDBFailure)
			if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); err == nil {
				t.Fatalf("Refresh with an injected %s failure = nil, want an error", method)
			}
		})
	}
}

// TestRefreshNoTxTreatsMissingUserOrDeviceAsInvalidToken exercises
// refreshNoTx's isNoRows branches for GetUserByID and GetDeviceByID
// (e.g. the user or device was deleted in the window between issuing the
// refresh token and this refresh call): both must be reported as the
// ordinary ErrInvalidRefreshToken, not a wrapped internal error.
func TestRefreshNoTxTreatsMissingUserOrDeviceAsInvalidToken(t *testing.T) {
	tests := []string{"GetUserByID", "GetDeviceByID"}

	for _, method := range tests {
		t.Run(method, func(t *testing.T) {
			svc, _ := newTestService(t)
			ctx := context.Background()
			fq := svc.Queries.(*fakeQuerier)

			registered, err := svc.Register(ctx, uniqueEmail("refresh-norows"), "correct-horse-battery-staple", testDevice("MacBook"))
			if err != nil {
				t.Fatalf("Register: %v", err)
			}

			fq.failNextCallTo(method, pgx.ErrNoRows)
			if _, err := svc.Refresh(ctx, registered.Tokens.RefreshToken, nil); !errors.Is(err, auth.ErrInvalidRefreshToken) {
				t.Fatalf("Refresh with an injected %s NoRows failure = %v, want ErrInvalidRefreshToken", method, err)
			}
		})
	}
}

// TestRefreshReuseDetectionWrapsUnexpectedDatabaseError exercises
// refreshNoTx's reuse-detection branch's own failure path: if revoking the
// token family itself fails, that failure must be surfaced rather than
// silently reported as ordinary reuse.
func TestRefreshReuseDetectionWrapsUnexpectedDatabaseError(t *testing.T) {
	svc, _ := newTestService(t)
	ctx := context.Background()
	fq := svc.Queries.(*fakeQuerier)

	registered, err := svc.Register(ctx, uniqueEmail("refresh-reuse-dberr"), "correct-horse-battery-staple", testDevice("MacBook"))
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	oldToken := registered.Tokens.RefreshToken

	if _, err := svc.Refresh(ctx, oldToken, nil); err != nil {
		t.Fatalf("first Refresh (rotates the token): %v", err)
	}

	// oldToken is now revoked; refreshing it again is reuse, and
	// RevokeRefreshTokenFamily is reuse-detection's own revocation call.
	fq.failNextCallTo("RevokeRefreshTokenFamily", errInjectedDBFailure)
	if _, err := svc.Refresh(ctx, oldToken, nil); err == nil || errors.Is(err, auth.ErrRefreshTokenReuse) {
		t.Fatalf("reuse Refresh with an injected RevokeRefreshTokenFamily failure = %v, want a wrapped unexpected error (not ErrRefreshTokenReuse)", err)
	}
}
