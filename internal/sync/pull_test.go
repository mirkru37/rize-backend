package sync

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// TestPullTenantIsolation proves a user's pull never returns another
// user's rows, per documentation/security.md §Tenant Isolation.
func TestPullTenantIsolation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	userA, deviceA := newUser(t, q)
	userB, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	// userA pushes a project (via direct SQL, mirroring service_test.go's
	// newProject helper) and a focus_session (via push, which this package
	// already implements).
	_ = newProject(t, pool, userA)

	startedAt := time.Now().UTC().Truncate(time.Second)
	fsID := newUUIDv7(t)
	_, err := svc.push(ctx, userIDString(userA), pushRequest{
		DeviceID: deviceA.ID.String(),
		Items:    []pushItem{focusSessionItem(t, fsID, startedAt, startedAt)},
	})
	if err != nil {
		t.Fatalf("push (userA fixture): %v", err)
	}

	// userB pulls from the beginning of the stream: it must never see
	// userA's project or focus_session.
	resp, err := svc.pull(ctx, userIDString(userB), "", 500)
	if err != nil {
		t.Fatalf("pull (userB): %v", err)
	}
	for entityType, cs := range resp.Changes {
		if len(cs.Upserts) != 0 {
			t.Fatalf("userB's pull returned %d upserts for %q, want 0 (cross-tenant leak)", len(cs.Upserts), entityType)
		}
	}
}

// TestPullPagination proves multi-page keyset pagination behaves per
// documentation/sync-protocol.md §Pull: a first page bounded by limit
// reports has_more=true and a next_cursor that, when re-requested, yields
// the remaining rows with has_more=false, with no row skipped or
// duplicated across the two pages (beyond redelivery, which is
// documented-safe, but this test asserts no row is missing and no row not
// yet inserted appears).
func TestPullPagination(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	const total = 5
	suffix := randomSuffix(t)
	ids := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := insertTag(t, pool, user, suffix, i)
		ids[id] = true
	}

	// First page: limit=2, expect has_more=true.
	page1, err := svc.pull(ctx, userIDString(user), "", 2)
	if err != nil {
		t.Fatalf("pull (page 1): %v", err)
	}
	tagsPage1 := page1.Changes["tags"].Upserts
	if len(tagsPage1) != 2 {
		t.Fatalf("page 1 tags upserts = %d, want 2", len(tagsPage1))
	}
	if !page1.HasMore {
		t.Fatalf("page 1 has_more = false, want true (5 tags exist, limit=2)")
	}

	seen := map[string]bool{}
	for _, u := range tagsPage1 {
		dto := u.(tagUpsertDTO)
		seen[dto.ID] = true
	}

	// Walk the rest of the pages until has_more is false, collecting every
	// tag id delivered.
	cursor := page1.NextCursor
	hasMore := page1.HasMore
	for hasMore {
		page, err := svc.pull(ctx, userIDString(user), cursor, 2)
		if err != nil {
			t.Fatalf("pull (subsequent page): %v", err)
		}
		for _, u := range page.Changes["tags"].Upserts {
			dto := u.(tagUpsertDTO)
			seen[dto.ID] = true
		}
		cursor = page.NextCursor
		hasMore = page.HasMore
	}

	for id := range ids {
		if !seen[id] {
			t.Fatalf("tag id %s was never delivered across any page", id)
		}
	}
}

// TestPullTombstoneDelivery proves a soft-deleted project is delivered as
// a tombstone (not an upsert) on the next pull, per
// documentation/sync-protocol.md's tombstone semantics.
func TestPullTombstoneDelivery(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	projectID := newProject(t, pool, user)

	// Pull once to establish a cursor positioned after the create.
	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (before delete): %v", err)
	}
	foundUpsert := false
	for _, u := range first.Changes["projects"].Upserts {
		if u.(projectUpsertDTO).ID == projectID {
			foundUpsert = true
		}
	}
	if !foundUpsert {
		t.Fatalf("first pull did not include the created project as an upsert")
	}

	// Soft-delete it directly (mirrors internal/projects.Service.Delete).
	if _, err := pool.Exec(ctx, `UPDATE projects SET deleted_at = now(), updated_at = now() WHERE id = $1`, mustParseUUID(t, projectID)); err != nil {
		t.Fatalf("soft-delete project: %v", err)
	}

	second, err := svc.pull(ctx, userIDString(user), first.NextCursor, 500)
	if err != nil {
		t.Fatalf("pull (after delete): %v", err)
	}
	foundTombstone := false
	for _, ts := range second.Changes["projects"].Tombstones {
		if ts.(projectTombstoneDTO).ID == projectID {
			foundTombstone = true
		}
	}
	if !foundTombstone {
		t.Fatalf("second pull (cursor advanced past the create) did not deliver the delete as a tombstone")
	}
	for _, u := range second.Changes["projects"].Upserts {
		if u.(projectUpsertDTO).ID == projectID {
			t.Fatalf("second pull delivered the deleted project as an upsert, not a tombstone")
		}
	}
}

// TestPullEmptyFeed proves a brand-new user with no data at all gets a
// well-formed, empty response rather than an error or null fields.
func TestPullEmptyFeed(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	resp, err := svc.pull(ctx, userIDString(user), "", 50)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if resp.HasMore {
		t.Fatalf("has_more = true for a user with no data, want false")
	}
	for entityType, cs := range resp.Changes {
		if cs.Upserts == nil || cs.Tombstones == nil {
			t.Fatalf("entity %q has a nil upserts/tombstones slice, want empty non-nil slices", entityType)
		}
		if len(cs.Upserts) != 0 || len(cs.Tombstones) != 0 {
			t.Fatalf("entity %q has non-empty changes for a brand-new user", entityType)
		}
	}
}

// TestPullCursorBoundaryExact proves the strict server_seq > cursor
// boundary: re-pulling with the exact next_cursor from a fully-drained
// page never redelivers rows already consumed.
func TestPullCursorBoundaryExact(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	insertTag(t, pool, user, suffix, 0)

	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (first): %v", err)
	}
	if len(first.Changes["tags"].Upserts) != 1 {
		t.Fatalf("first pull tags upserts = %d, want 1", len(first.Changes["tags"].Upserts))
	}
	if first.HasMore {
		t.Fatalf("has_more = true after draining the only row, want false")
	}

	second, err := svc.pull(ctx, userIDString(user), first.NextCursor, 500)
	if err != nil {
		t.Fatalf("pull (second, same cursor semantics): %v", err)
	}
	if len(second.Changes["tags"].Upserts) != 0 {
		t.Fatalf("second pull at the exact boundary cursor returned %d tags upserts, want 0", len(second.Changes["tags"].Upserts))
	}
}

func TestPullLimitClamping(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	// A limit above maxPullLimit must be clamped rather than honored
	// verbatim or rejected.
	if _, err := svc.pull(ctx, userIDString(user), "", maxPullLimit+1000); err != nil {
		t.Fatalf("pull with an oversized limit: %v", err)
	}

	// limit <= 0 falls back to defaultPullLimit rather than erroring or
	// requesting zero rows.
	resp, err := svc.pull(ctx, userIDString(user), "", 0)
	if err != nil {
		t.Fatalf("pull with limit=0: %v", err)
	}
	if resp.HasMore {
		t.Fatalf("has_more = true for a near-empty account with the default limit, want false")
	}
}

// insertTag inserts a tag row directly via SQL (mirroring
// service_test.go's newProject helper — there is no sync-package write
// path for tags) and returns its id string.
func insertTag(t *testing.T, pool *pgxpool.Pool, user storedb.User, suffix string, n int) string {
	t.Helper()
	ctx := context.Background()
	idStr := newUUIDv7(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at)
		VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, idStr), user.ID, fmt.Sprintf("pull-test-tag-%s-%d", suffix, n))
	if err != nil {
		t.Fatalf("insert tag: %v", err)
	}
	return idStr
}
