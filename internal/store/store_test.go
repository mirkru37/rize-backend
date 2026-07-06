package store_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// runID is a random suffix mixed into every unique field (emails, etc.)
// this test binary creates, so the integration suite can be run repeatedly
// against the same long-lived database without unique-constraint
// collisions from a prior run's leftover rows.
var runID = randomHex(8)

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

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

// pgErrCode returns the SQLSTATE code of err if it (or something it wraps)
// is a *pgconn.PgError, and "" otherwise.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

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
		{name: "regular user", email: fmt.Sprintf("alice+%s@example.com", runID), role: "user"},
		{name: "admin user", email: fmt.Sprintf("bob+%s@example.com", runID), role: "admin"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			created, err := q.CreateUser(ctx, storedb.CreateUserParams{
				Email:       textPtr(tt.email),
				DisplayName: textPtr(tt.name),
				Role:        tt.role,
				Timezone:    textPtr("UTC"),
			})
			if err != nil {
				t.Fatalf("CreateUser: %v", err)
			}
			t.Cleanup(func() {
				_ = q.SoftDeleteUser(ctx, created.ID)
			})

			if created.Role != tt.role {
				t.Errorf("Role = %q, want %q", created.Role, tt.role)
			}
			if created.ServerSeq <= 0 {
				t.Errorf("ServerSeq = %d, want a positive value assigned by the DB default", created.ServerSeq)
			}

			fetched, err := q.GetUserByID(ctx, created.ID)
			if err != nil {
				t.Fatalf("GetUserByID: %v", err)
			}
			if fetched.DisplayName == nil || *fetched.DisplayName != tt.name {
				t.Errorf("DisplayName = %v, want %q", fetched.DisplayName, tt.name)
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
				DisplayName: textPtr(tt.name + " updated"),
				Timezone:    textPtr("America/New_York"),
			})
			if err != nil {
				t.Fatalf("UpdateUserProfile: %v", err)
			}
			if updated.DisplayName == nil || *updated.DisplayName != tt.name+" updated" {
				t.Errorf("DisplayName after update = %v, want %q", updated.DisplayName, tt.name+" updated")
			}
			if updated.ServerSeq <= created.ServerSeq {
				t.Errorf("ServerSeq after update = %d, want a value greater than the insert's %d", updated.ServerSeq, created.ServerSeq)
			}

			if err := q.SoftDeleteUser(ctx, created.ID); err != nil {
				t.Fatalf("SoftDeleteUser: %v", err)
			}

			if _, err := q.GetUserByID(ctx, created.ID); err == nil {
				t.Error("GetUserByID after soft delete should return an error (no matching row), got nil")
			}
		})
	}
}

// TestUserNullableProfileFields verifies that display_name and timezone can
// be omitted at creation time, per documentation/database-schema.md (which
// lists both columns without a NOT NULL constraint) — an Apple Sign In user
// may authenticate without ever supplying either.
func TestUserNullableProfileFields(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	email := fmt.Sprintf("apple-signin+%s@example.com", runID)
	created, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(email),
		DisplayName: nil,
		Role:        "user",
		Timezone:    nil,
	})
	if err != nil {
		t.Fatalf("CreateUser with nil display_name/timezone: %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, created.ID)
	})

	if created.DisplayName != nil {
		t.Errorf("DisplayName = %v, want nil", created.DisplayName)
	}
	if created.Timezone != nil {
		t.Errorf("Timezone = %v, want nil", created.Timezone)
	}
}

func TestDeviceAndRefreshTokenQueries(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("carol+%s@example.com", runID)),
		DisplayName: textPtr("Carol"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, user.ID)
	})

	// A second user is used to prove that device queries are tenant-scoped:
	// otherUser must never be able to read, touch, or revoke a device that
	// belongs to user (documentation/security.md §Tenant Isolation).
	otherUser, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("mallory+%s@example.com", runID)),
		DisplayName: textPtr("Mallory"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser (otherUser): %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, otherUser.ID)
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
				_ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID})
			})

			if device.Platform != tt.platform {
				t.Errorf("Platform = %q, want %q", device.Platform, tt.platform)
			}

			fetched, err := q.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: device.ID, UserID: user.ID})
			if err != nil {
				t.Fatalf("GetDeviceByID: %v", err)
			}
			if fetched.UserID != user.ID {
				t.Errorf("UserID = %v, want %v", fetched.UserID, user.ID)
			}

			// Cross-tenant reads/writes must fail: otherUser cannot see,
			// touch, or revoke user's device.
			if _, err := q.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: device.ID, UserID: otherUser.ID}); err == nil {
				t.Error("GetDeviceByID scoped to a different user_id should return no rows, got a device")
			}
			if err := q.TouchDeviceLastSeen(ctx, storedb.TouchDeviceLastSeenParams{ID: device.ID, UserID: otherUser.ID}); err != nil {
				t.Fatalf("TouchDeviceLastSeen (cross-tenant, expected no-op not error): %v", err)
			}
			if err := q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: otherUser.ID}); err != nil {
				t.Fatalf("RevokeDevice (cross-tenant, expected no-op not error): %v", err)
			}
			// The device must still be live for its real owner after the
			// cross-tenant revoke attempt above was silently ignored.
			if _, err := q.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: device.ID, UserID: user.ID}); err != nil {
				t.Fatalf("GetDeviceByID after cross-tenant revoke attempt: %v", err)
			}

			if err := q.TouchDeviceLastSeen(ctx, storedb.TouchDeviceLastSeenParams{ID: device.ID, UserID: user.ID}); err != nil {
				t.Fatalf("TouchDeviceLastSeen: %v", err)
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
				TokenHash: []byte(fmt.Sprintf("hashed-token-%s-%s", tt.platform, runID)),
				FamilyID:  familyID,
				ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(30 * 24 * time.Hour), Valid: true},
			})
			if err != nil {
				t.Fatalf("CreateRefreshToken: %v", err)
			}

			byHash, err := q.GetRefreshTokenByHash(ctx, []byte(fmt.Sprintf("hashed-token-%s-%s", tt.platform, runID)))
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

// TestConstraintViolations is table-driven coverage for the CHECK and
// UNIQUE constraints defined in the schema migrations, asserting the
// expected Postgres SQLSTATE is surfaced through pgx.
func TestConstraintViolations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("dave+%s@example.com", runID)),
		DisplayName: textPtr("Dave"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, user.ID)
	})

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID:     user.ID,
		Platform:   "macos",
		Name:       "constraint-test-device",
		Model:      "test-model",
		OsVersion:  "1.0",
		AppVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	t.Cleanup(func() {
		_ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID})
	})

	t.Run("invalid role violates the users role CHECK constraint", func(t *testing.T) {
		_, err := q.CreateUser(ctx, storedb.CreateUserParams{
			Email:       textPtr(fmt.Sprintf("bad-role+%s@example.com", runID)),
			DisplayName: textPtr("Bad Role"),
			Role:        "superadmin",
			Timezone:    textPtr("UTC"),
		})
		if err == nil {
			t.Fatal("expected a CHECK violation, got nil error")
		}
		if code := pgErrCode(err); code != "23514" {
			t.Fatalf("SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
		}
	})

	t.Run("invalid platform violates the devices platform CHECK constraint", func(t *testing.T) {
		_, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
			UserID:     user.ID,
			Platform:   "windows",
			Name:       "bad-platform-device",
			Model:      "test-model",
			OsVersion:  "1.0",
			AppVersion: "1.0",
		})
		if err == nil {
			t.Fatal("expected a CHECK violation, got nil error")
		}
		if code := pgErrCode(err); code != "23514" {
			t.Fatalf("SQLSTATE = %q, want 23514 (check_violation): %v", code, err)
		}
	})

	t.Run("duplicate activity_events idempotency key violates the UNIQUE constraint", func(t *testing.T) {
		eventID := newUUIDv4(t, pool)
		startedAt := time.Now().Truncate(time.Second)
		endedAt := startedAt.Add(5 * time.Minute)

		insertActivityEvent := func() error {
			_, err := pool.Exec(ctx, `
				INSERT INTO activity_events (
					event_id, user_id, device_id, started_at, ended_at,
					type, source, inserted_at, server_seq
				) VALUES (
					$1, $2, $3, $4, $5,
					'app_active', 'desktop', now(), nextval('server_seq_global')
				)`,
				eventID, user.ID, device.ID, startedAt, endedAt,
			)
			return err
		}

		if err := insertActivityEvent(); err != nil {
			t.Fatalf("first insert (expected success): %v", err)
		}

		err := insertActivityEvent()
		if err == nil {
			t.Fatal("expected a UNIQUE violation on the second insert with the same idempotency key, got nil")
		}
		if code := pgErrCode(err); code != "23505" {
			t.Fatalf("SQLSTATE = %q, want 23505 (unique_violation): %v", code, err)
		}
	})
}

// TestActivityEventDurationGeneratedColumn round-trips an activity_events
// insert through raw pgx (there is no sqlc query for this table yet) and
// asserts the generated duration_s column matches
// EXTRACT(EPOCH FROM ended_at - started_at)::int, per
// documentation/database-schema.md.
func TestActivityEventDurationGeneratedColumn(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("erin+%s@example.com", runID)),
		DisplayName: textPtr("Erin"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, user.ID)
	})

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID:     user.ID,
		Platform:   "macos",
		Name:       "duration-test-device",
		Model:      "test-model",
		OsVersion:  "1.0",
		AppVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	t.Cleanup(func() {
		_ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID})
	})

	eventID := newUUIDv4(t, pool)
	startedAt := time.Now().Truncate(time.Second)
	wantDuration := int32(17 * 60)
	endedAt := startedAt.Add(time.Duration(wantDuration) * time.Second)

	_, err = pool.Exec(ctx, `
		INSERT INTO activity_events (
			event_id, user_id, device_id, started_at, ended_at,
			type, source, inserted_at, server_seq
		) VALUES (
			$1, $2, $3, $4, $5,
			'app_active', 'desktop', now(), nextval('server_seq_global')
		)`,
		eventID, user.ID, device.ID, startedAt, endedAt,
	)
	if err != nil {
		t.Fatalf("insert activity_events: %v", err)
	}

	var gotDuration int32
	err = pool.QueryRow(ctx, `
		SELECT duration_s FROM activity_events
		WHERE user_id = $1 AND event_id = $2 AND started_at = $3`,
		user.ID, eventID, startedAt,
	).Scan(&gotDuration)
	if err != nil {
		t.Fatalf("select duration_s: %v", err)
	}

	if gotDuration != wantDuration {
		t.Errorf("duration_s = %d, want %d (EXTRACT(EPOCH FROM ended_at - started_at)::int)", gotDuration, wantDuration)
	}
}

// TestGlobalServerSeqSequence asserts that server_seq values are assigned
// from the single shared sequence described in documentation/sync-protocol.md
// ("the server's global server_seq sequence space"): an insert that omits
// server_seq gets a monotonically increasing value, and two inserts across
// different tables get strictly increasing values from the same space.
func TestGlobalServerSeqSequence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	userA, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("frank+%s@example.com", runID)),
		DisplayName: textPtr("Frank"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser (userA): %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, userA.ID)
	})

	// A second insert into the SAME table (users) must get a strictly
	// greater server_seq than the first.
	userB, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("grace+%s@example.com", runID)),
		DisplayName: textPtr("Grace"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser (userB): %v", err)
	}
	t.Cleanup(func() {
		_ = q.SoftDeleteUser(ctx, userB.ID)
	})

	if userB.ServerSeq <= userA.ServerSeq {
		t.Fatalf("userB.ServerSeq (%d) should be strictly greater than userA.ServerSeq (%d)", userB.ServerSeq, userA.ServerSeq)
	}

	// A subsequent insert into a DIFFERENT table (categories) must also
	// draw a strictly greater value from the same global sequence space.
	var categorySeq int64
	err = pool.QueryRow(ctx, `
		INSERT INTO categories (id, user_id, name, color, productivity, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, 'Deep Work', '#336699', 2, now(), now())
		RETURNING server_seq`,
		userA.ID,
	).Scan(&categorySeq)
	if err != nil {
		t.Fatalf("insert categories: %v", err)
	}

	if categorySeq <= userB.ServerSeq {
		t.Fatalf("categories.server_seq (%d) should be strictly greater than the preceding users.server_seq (%d): both must be drawn from the same global sequence", categorySeq, userB.ServerSeq)
	}
}

// newUUIDv4 asks Postgres to mint a UUID rather than pulling in a new Go
// module dependency just for test data.
func newUUIDv4(t *testing.T, pool *pgxpool.Pool) pgtype.UUID {
	t.Helper()

	var id pgtype.UUID
	if err := pool.QueryRow(context.Background(), "SELECT gen_random_uuid()").Scan(&id); err != nil {
		t.Fatalf("gen_random_uuid: %v", err)
	}
	return id
}
