package store_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// testPool returns a pool connected to DATABASE_URL, or skips the test when
// DATABASE_URL is unset, so DB-backed tests stay opt-in locally / in CI
// environments with a real TimescaleDB container and green (skipped)
// otherwise.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping store integration test")
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

func textPtr(s string) *string { return &s }

func TestNewPool(t *testing.T) {
	t.Run("empty DSN returns nil pool and nil error", func(t *testing.T) {
		pool, err := store.NewPool(context.Background(), "")
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if pool != nil {
			t.Fatalf("expected nil pool, got %v", pool)
		}
	})

	t.Run("invalid DSN returns an error", func(t *testing.T) {
		_, err := store.NewPool(context.Background(), "://not-a-valid-dsn")
		if err == nil {
			t.Fatal("expected an error for an invalid DSN, got nil")
		}
	})
}

func TestUserQueries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	tests := []struct {
		name  string
		email string
		role  string
	}{
		{name: "regular user", email: "alice+store-test@example.com", role: "user"},
		{name: "admin user", email: "bob+store-test@example.com", role: "admin"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			created, err := q.CreateUser(ctx, storedb.CreateUserParams{
				Email:       textPtr(tt.email),
				DisplayName: tt.name,
				Role:        tt.role,
				Timezone:    "UTC",
				ServerSeq:   1,
			})
			if err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			t.Cleanup(func() {
				_ = q.SoftDeleteUser(ctx, storedb.SoftDeleteUserParams{ID: created.ID, ServerSeq: 2})
			})

			if created.Role != tt.role {
				t.Errorf("Role = %q, want %q", created.Role, tt.role)
			}

			fetched, err := q.GetUserByID(ctx, created.ID)
			if err != nil {
				t.Fatalf("GetUserByID: %v", err)
			}
			if fetched.DisplayName != tt.name {
				t.Errorf("DisplayName = %q, want %q", fetched.DisplayName, tt.name)
			}

			byEmail, err := q.GetUserByEmail(ctx, textPtr(tt.email))
			if err != nil {
				t.Fatalf("GetUserByEmail: %v", err)
			}
			if byEmail.ID != created.ID {
				t.Errorf("GetUserByEmail returned a different row than CreateUser produced")
			}

			updated, err := q.UpdateUserProfile(ctx, storedb.UpdateUserProfileParams{
				ID:          created.ID,
				DisplayName: tt.name + " updated",
				Timezone:    "America/New_York",
				ServerSeq:   3,
			})
			if err != nil {
				t.Fatalf("UpdateUserProfile: %v", err)
			}
			if updated.DisplayName != tt.name+" updated" {
				t.Errorf("DisplayName after update = %q, want %q", updated.DisplayName, tt.name+" updated")
			}

			if err := q.SoftDeleteUser(ctx, storedb.SoftDeleteUserParams{ID: created.ID, ServerSeq: 4}); err != nil {
				t.Fatalf("SoftDeleteUser: %v", err)
			}

			if _, err := q.GetUserByID(ctx, created.ID); err == nil {
				t.Error("GetUserByID after soft delete should return an error (no matching row), got nil")
			}
		})
	}
}

func TestDeviceAndRefreshTokenQueries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr("carol+store-test@example.com"),
		DisplayName: "Carol",
		Role:        "user",
		Timezone:    "UTC",
		ServerSeq:   1,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, storedb.SoftDeleteUserParams{ID: user.ID, ServerSeq: 99})
	})

	tests := []struct {
		name     string
		platform string
	}{
		{name: "macos device", platform: "macos"},
		{name: "ios device", platform: "ios"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
				UserID:     user.ID,
				Platform:   tt.platform,
				Name:       tt.name,
				Model:      "test-model",
				OsVersion:  "1.0",
				AppVersion: "1.0",
			})
			if err != nil {
				t.Fatalf("CreateDevice: %v", err)
			}
			t.Cleanup(func() {
				_ = q.RevokeDevice(ctx, device.ID)
			})

			if device.Platform != tt.platform {
				t.Errorf("Platform = %q, want %q", device.Platform, tt.platform)
			}

			fetched, err := q.GetDeviceByID(ctx, device.ID)
			if err != nil {
				t.Fatalf("GetDeviceByID: %v", err)
			}
			if fetched.UserID != user.ID {
				t.Errorf("UserID = %v, want %v", fetched.UserID, user.ID)
			}

			devices, err := q.ListDevicesByUser(ctx, user.ID)
			if err != nil {
				t.Fatalf("ListDevicesByUser: %v", err)
			}
			found := false
			for _, d := range devices {
				if d.ID == device.ID {
					found = true
				}
			}
			if !found {
				t.Error("ListDevicesByUser did not include the created device")
			}

			familyID := pgtype.UUID{}
			if err := familyID.Scan(device.ID.String()); err != nil {
				t.Fatalf("familyID.Scan: %v", err)
			}

			token, err := q.CreateRefreshToken(ctx, storedb.CreateRefreshTokenParams{
				UserID:    user.ID,
				DeviceID:  device.ID,
				TokenHash: []byte("hashed-token-" + tt.platform),
				FamilyID:  familyID,
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true},
			})
			if err != nil {
				t.Fatalf("CreateRefreshToken: %v", err)
			}

			byHash, err := q.GetRefreshTokenByHash(ctx, []byte("hashed-token-"+tt.platform))
			if err != nil {
				t.Fatalf("GetRefreshTokenByHash: %v", err)
			}
			if byHash.ID != token.ID {
				t.Errorf("GetRefreshTokenByHash returned a different row than CreateRefreshToken produced")
			}

			active, err := q.ListActiveRefreshTokensByUser(ctx, user.ID)
			if err != nil {
				t.Fatalf("ListActiveRefreshTokensByUser: %v", err)
			}
			if len(active) == 0 {
				t.Error("ListActiveRefreshTokensByUser returned no rows for a freshly created, unexpired token")
			}

			if err := q.RevokeRefreshTokenFamily(ctx, familyID); err != nil {
				t.Fatalf("RevokeRefreshTokenFamily: %v", err)
			}

			activeAfterRevoke, err := q.ListActiveRefreshTokensByUser(ctx, user.ID)
			if err != nil {
				t.Fatalf("ListActiveRefreshTokensByUser after revoke: %v", err)
			}
			for _, rt := range activeAfterRevoke {
				if rt.ID == token.ID {
					t.Error("revoked token still appears in ListActiveRefreshTokensByUser")
				}
			}
		})
	}
}
