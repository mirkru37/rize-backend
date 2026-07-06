package focussessions

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping focussessions integration test")
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

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func textPtr(s string) *string { return &s }

func newUserAndDevice(t *testing.T, q *storedb.Queries) (storedb.User, storedb.Device) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)
	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("focussessions-test+%s@example.com", suffix)),
		DisplayName: textPtr("Focus Sessions Test User"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(ctx, user.ID) })

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID: user.ID, Platform: "macos", Name: "focussessions-test-device",
		Model: "test-model", OsVersion: "1.0", AppVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	t.Cleanup(func() { _ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID}) })
	return user, device
}

func userIDString(u storedb.User) string { return u.ID.String() }

func TestFocusSessionsHappyPath(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	created, err := svc.Create(ctx, userIDString(user), createRequest{
		DeviceID:  device.ID.String(),
		Kind:      "focus",
		StartedAt: startedAt.Format(timeLayout),
		Status:    "running",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.DeviceID != device.ID {
		t.Fatalf("Create device_id = %v, want %v", created.DeviceID, device.ID)
	}

	got, err := svc.Get(ctx, userIDString(user), created.ID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != "running" {
		t.Fatalf("Get status = %q, want running", got.Status)
	}

	updated, err := svc.Update(ctx, userIDString(user), created.ID.String(), updateRequest{Status: textPtr("completed")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Status != "completed" {
		t.Fatalf("Update status = %q, want completed", updated.Status)
	}
	if updated.ServerSeq <= created.ServerSeq {
		t.Fatalf("Update did not bump server_seq: before=%d after=%d", created.ServerSeq, updated.ServerSeq)
	}

	if err := svc.Delete(ctx, userIDString(user), created.ID.String()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, userIDString(user), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

// TestFocusSessionsDeviceTenantIsolation proves a device_id belonging to
// another user is rejected exactly like an unknown one, per
// documentation/security.md §Tenant Isolation.
func TestFocusSessionsDeviceTenantIsolation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUserAndDevice(t, q)
	_, otherDevice := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	_, err := svc.Create(ctx, userIDString(user), createRequest{
		DeviceID:  otherDevice.ID.String(),
		Kind:      "focus",
		StartedAt: startedAt.Format(timeLayout),
		Status:    "running",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (another user's device_id) = %v, want ErrValidation", err)
	}
}

// TestFocusSessionsProjectTenantIsolation proves a project_id belonging to
// another user is rejected the same way.
func TestFocusSessionsProjectTenantIsolation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUserAndDevice(t, q)
	other, _ := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	otherProjectID := insertProject(t, pool, other)

	startedAt := time.Now().UTC().Truncate(time.Second)
	_, err := svc.Create(ctx, userIDString(user), createRequest{
		DeviceID:  device.ID.String(),
		ProjectID: otherProjectID,
		Kind:      "focus",
		StartedAt: startedAt.Format(timeLayout),
		Status:    "running",
	})
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (another user's project_id) = %v, want ErrValidation", err)
	}
}

func TestFocusSessionsValidation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	base := createRequest{DeviceID: device.ID.String(), StartedAt: startedAt.Format(timeLayout)}

	cases := []struct {
		name string
		req  createRequest
	}{
		{"invalid kind", func() createRequest { r := base; r.Kind = "nap"; r.Status = "running"; return r }()},
		{"invalid status", func() createRequest { r := base; r.Kind = "focus"; r.Status = "napping"; return r }()},
		{"invalid started_at", func() createRequest {
			r := base
			r.Kind = "focus"
			r.Status = "running"
			r.StartedAt = "not-a-time"
			return r
		}()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, userIDString(user), tc.req); !errors.Is(err, ErrValidation) {
				t.Fatalf("Create(%+v) = %v, want ErrValidation", tc.req, err)
			}
		})
	}
}

func TestFocusSessionsCrossTenant404Equivalence(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	owner, ownerDevice := newUserAndDevice(t, q)
	other, _ := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	created, err := svc.Create(ctx, userIDString(owner), createRequest{
		DeviceID:  ownerDevice.ID.String(),
		Kind:      "focus",
		StartedAt: startedAt.Format(timeLayout),
		Status:    "running",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Get(ctx, userIDString(other), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get (other user) = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, userIDString(other), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete (other user) = %v, want ErrNotFound", err)
	}
}

func TestFocusSessionsListPagination(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUserAndDevice(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, userIDString(user), createRequest{
			DeviceID:  device.ID.String(),
			Kind:      "focus",
			StartedAt: startedAt.Add(time.Duration(i) * time.Minute).Format(timeLayout),
			Status:    "running",
		}); err != nil {
			t.Fatalf("Create (fixture %d): %v", i, err)
		}
	}

	page1, cursor1, hasMore1, err := svc.List(ctx, userIDString(user), "", 2)
	if err != nil {
		t.Fatalf("List (page 1): %v", err)
	}
	if len(page1) != 2 || !hasMore1 {
		t.Fatalf("page 1 = (%d items, hasMore=%v), want (2, true)", len(page1), hasMore1)
	}

	page2, _, hasMore2, err := svc.List(ctx, userIDString(user), cursor1, 2)
	if err != nil {
		t.Fatalf("List (page 2): %v", err)
	}
	if len(page2) != 1 || hasMore2 {
		t.Fatalf("page 2 = (%d items, hasMore=%v), want (1, false)", len(page2), hasMore2)
	}
}

// insertProject inserts a project row directly via SQL, mirroring
// internal/sync/service_test.go's newProject helper.
func insertProject(t *testing.T, pool *pgxpool.Pool, user storedb.User) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, user_id, name, color, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, '#ffffff', now(), now())
		RETURNING id`, user.ID, "fs-test-project-"+randomSuffix(t)).Scan(&id)
	if err != nil {
		t.Fatalf("insert project: %v", err)
	}
	return id
}
