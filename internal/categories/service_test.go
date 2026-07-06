package categories

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
		t.Skip("DATABASE_URL not set; skipping categories integration test")
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
		Email:       textPtr(fmt.Sprintf("categories-test+%s@example.com", suffix)),
		DisplayName: textPtr("Categories Test User"),
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

// insertSystemCategory inserts a system-default category (user_id NULL)
// directly via SQL — there's no API path to create one, per doc.go.
func insertSystemCategory(t *testing.T, pool *pgxpool.Pool, suffix string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO categories (id, user_id, name, color, productivity, created_at, updated_at)
		VALUES (gen_random_uuid(), NULL, $1, '#000000', 0, now(), now())
		RETURNING id`, "system-cat-"+suffix).Scan(&id)
	if err != nil {
		t.Fatalf("insert system category: %v", err)
	}
	return id
}

func TestCategoriesHappyPath(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	created, err := svc.Create(ctx, userIDString(user), createRequest{Name: "Deep Work", Color: "#123456", Productivity: 2})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.UserID.Valid {
		t.Fatalf("a category created via the API must never be a system default")
	}

	got, err := svc.Get(ctx, userIDString(user), created.ID.String())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != created.Name {
		t.Fatalf("Get name = %q, want %q", got.Name, created.Name)
	}

	updated, err := svc.Update(ctx, userIDString(user), created.ID.String(), updateRequest{Productivity: int16Ptr(-1)})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Productivity != -1 {
		t.Fatalf("Update productivity = %d, want -1", updated.Productivity)
	}

	if err := svc.Delete(ctx, userIDString(user), created.ID.String()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Get(ctx, userIDString(user), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrNotFound", err)
	}
}

// TestCategoriesProductivityValidation proves the -2..2 CHECK constraint
// is enforced at the service layer (client-visible 400, never a raw DB
// error).
func TestCategoriesProductivityValidation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: "Bad", Color: "#fff", Productivity: 3}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (productivity=3) = %v, want ErrValidation", err)
	}
	if _, err := svc.Create(ctx, userIDString(user), createRequest{Name: "Bad", Color: "#fff", Productivity: -3}); !errors.Is(err, ErrValidation) {
		t.Fatalf("Create (productivity=-3) = %v, want ErrValidation", err)
	}
}

// TestSystemCategoriesReadableButNotEditable proves a system default
// (user_id IS NULL) is visible via List/Get but PATCH/DELETE against it
// reports 404, per doc.go's explicit design.
func TestSystemCategoriesReadableButNotEditable(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	systemID := insertSystemCategory(t, pool, suffix)

	got, err := svc.Get(ctx, userIDString(user), systemID)
	if err != nil {
		t.Fatalf("Get (system category) = %v, want success (readable)", err)
	}
	if got.UserID.Valid {
		t.Fatalf("Get (system category).UserID.Valid = true, want false (system default)")
	}

	if _, err := svc.Update(ctx, userIDString(user), systemID, updateRequest{Name: textPtr("hijacked")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update (system category) = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, userIDString(user), systemID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete (system category) = %v, want ErrNotFound", err)
	}

	// List must include the system default alongside the user's own.
	items, _, _, err := svc.List(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	found := false
	for _, c := range items {
		if c.ID.String() == systemID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List did not include the system default category %s", systemID)
	}
}

func TestCategoriesCrossTenant404Equivalence(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	owner := newUser(t, q)
	other := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	created, err := svc.Create(ctx, userIDString(owner), createRequest{Name: "Owner cat", Color: "#fff", Productivity: 0})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Update(ctx, userIDString(other), created.ID.String(), updateRequest{Name: textPtr("hijacked")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update (other user) = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(ctx, userIDString(other), created.ID.String()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete (other user) = %v, want ErrNotFound", err)
	}
}

func int16Ptr(v int16) *int16 { return &v }
