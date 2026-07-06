package tags

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
		t.Skip("DATABASE_URL not set; skipping tags integration test")
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
		Email:       textPtr(fmt.Sprintf("tags-test+%s@example.com", suffix)),
		DisplayName: textPtr("Tags Test User"),
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

func TestTagsHappyPath(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	created, err := svc.Create(ctx, userIDString(user), createRequest{Name: "urgent-" + suffix})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := svc.Get(ctx, userIDString(user), created.ID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != created.Name {
		t.Fatalf("Get name = %q, want %q", got.Name, created.Name)
	}

	updated, err := svc.Update(ctx, userIDString(user), created.ID.String(), updateRequest{Name: textPtr("renamed-" + suffix)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "renamed-"+suffix {
		t.Fatalf("Update name = %q, want %q", updated.Name, "renamed-"+suffix)
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

// TestTagsUniqueNameConflict proves tags' UNIQUE (user_id, name) violation
// surfaces as ErrConflict, per documentation/database-schema.md.
func TestTagsUniqueNameConflict(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	name := "duplicate-" + suffix
	if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: name}); err != nil {
		t.Fatalf("Create (first): %v", err)
	}
	if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: name}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create (duplicate name) = %v, want ErrConflict", err)
	}

	// A different user can still use the same name.
	other := newUser(t, q)
	if _, err := svc.Create(ctx, userIDString(other), createRequest{Name: name}); err != nil {
		t.Fatalf("Create (other user, same name) = %v, want success", err)
	}
}

func TestTagsCrossTenant404Equivalence(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	owner := newUser(t, q)
	other := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	created, err := svc.Create(ctx, userIDString(owner), createRequest{Name: "owner-tag-" + randomSuffix(t)})
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

func TestTagsListPagination(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	for i := 0; i < 3; i++ {
		if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: fmt.Sprintf("tag-%s-%d", suffix, i)}); err != nil {
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
