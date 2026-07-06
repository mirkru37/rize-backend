package projects

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
		t.Skip("DATABASE_URL not set; skipping projects integration test")
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

func newUser(t *testing.T, q *storedb.Queries) storedb.User {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)
	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("projects-test+%s@example.com", suffix)),
		DisplayName: textPtr("Projects Test User"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(ctx, user.ID) })
	return user
}

func userIDString(u storedb.User) string { return u.ID.String() }

// TestCreateGetUpdateDeleteHappyPath exercises the full CRUD lifecycle for
// a single project.
func TestCreateGetUpdateDeleteHappyPath(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	created, err := svc.Create(ctx, userIDString(user), createRequest{Name: "Deep Work", Color: "#112233"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Name != "Deep Work" || created.Color != "#112233" {
		t.Fatalf("Create result = %+v, want name/color to match the request", created)
	}
	if created.ServerSeq == 0 {
		t.Fatalf("Create result has server_seq = 0, want a bumped value")
	}

	got, err := svc.Get(ctx, userIDString(user), created.ID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != created.ID {
		t.Fatalf("Get returned id %v, want %v", got.ID, created.ID)
	}

	updated, err := svc.Update(ctx, userIDString(user), created.ID.String(), updateRequest{Name: textPtr("Renamed")})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("Update result name = %q, want %q", updated.Name, "Renamed")
	}
	if updated.Color != created.Color {
		t.Fatalf("Update result color = %q, want unchanged %q (partial update)", updated.Color, created.Color)
	}
	if !updated.UpdatedAt.Time.After(created.UpdatedAt.Time) {
		t.Fatalf("Update did not advance updated_at: before=%v after=%v", created.UpdatedAt.Time, updated.UpdatedAt.Time)
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

// TestCreateValidation proves blank required fields are rejected.
func TestCreateValidation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	cases := []struct {
		name string
		req  createRequest
	}{
		{"blank name", createRequest{Name: "  ", Color: "#fff"}},
		{"blank color", createRequest{Name: "Name", Color: ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Create(ctx, userIDString(user), tc.req); !errors.Is(err, ErrValidation) {
				t.Fatalf("Create(%+v) = %v, want ErrValidation", tc.req, err)
			}
		})
	}
}

// TestCrossTenant404Equivalence proves a project belonging to another user
// is reported identically to a nonexistent id, for Get/Update/Delete, per
// documentation/security.md §Tenant Isolation.
func TestCrossTenant404Equivalence(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	owner := newUser(t, q)
	other := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	created, err := svc.Create(ctx, userIDString(owner), createRequest{Name: "Owner's project", Color: "#000"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Get(ctx, userIDString(other), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get (other user) = %v, want ErrNotFound", err)
	}
	if _, err := svc.Update(ctx, userIDString(other), created.ID.String(), updateRequest{Name: textPtr("hijacked")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update (other user) = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, userIDString(other), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete (other user) = %v, want ErrNotFound", err)
	}

	// A syntactically-valid but nonexistent id must report the exact same
	// error.
	nonexistent := "018f9a3e-2b4b-7c31-9a2e-6f1d2b3c4d5e"
	if _, err := svc.Get(ctx, userIDString(owner), nonexistent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get (nonexistent id) = %v, want ErrNotFound", err)
	}

	// The owner's project must still be intact (not mutated by the other
	// user's failed Update attempt).
	stillThere, err := svc.Get(ctx, userIDString(owner), created.ID.String())
	if err != nil {
		t.Fatalf("Get (owner, after other's failed update): %v", err)
	}
	if stillThere.Name != "Owner's project" {
		t.Fatalf("owner's project name = %q, want unchanged %q", stillThere.Name, "Owner's project")
	}
}

// TestListPaginationAndTenantIsolation proves ListProjects paginates via
// the cursor convention and never returns another user's rows.
func TestListPaginationAndTenantIsolation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	other := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: fmt.Sprintf("Project %d", i), Color: "#abc"}); err != nil {
			t.Fatalf("Create (fixture %d): %v", i, err)
		}
	}
	if _, err := svc.Create(ctx, userIDString(other), createRequest{Name: "Other's project", Color: "#abc"}); err != nil {
		t.Fatalf("Create (other user's fixture): %v", err)
	}

	page1, cursor1, hasMore1, err := svc.List(ctx, userIDString(user), "", 2)
	if err != nil {
		t.Fatalf("List (page 1): %v", err)
	}
	if len(page1) != 2 {
		t.Fatalf("page 1 length = %d, want 2", len(page1))
	}
	if !hasMore1 {
		t.Fatalf("page 1 has_more = false, want true (3 projects exist, limit=2)")
	}

	page2, _, hasMore2, err := svc.List(ctx, userIDString(user), cursor1, 2)
	if err != nil {
		t.Fatalf("List (page 2): %v", err)
	}
	if len(page2) != 1 {
		t.Fatalf("page 2 length = %d, want 1", len(page2))
	}
	if hasMore2 {
		t.Fatalf("page 2 has_more = true, want false (fully drained)")
	}

	for _, p := range append(page1, page2...) {
		if p.UserID != user.ID {
			t.Fatalf("List returned a project not owned by the requesting user: %+v", p)
		}
	}
}
