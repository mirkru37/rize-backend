package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// TestPullTenantIsolation proves a user's pull never returns another
// user's rows, per documentation/security.md §Tenant Isolation.
func TestPullTenantIsolation(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	userA, deviceA := newUser(t, q)
	userB, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	// userA pushes a project (via direct SQL, mirroring service_test.go's
	// newProject helper) and a focus_session (via push, which this package
	// already implements), and directly inserts a user-owned category.
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
	userACategoryID := insertUserCategory(t, pool, userA, randomSuffix(t))

	// userB pulls from the beginning of the stream: it must never see
	// userA's project, focus_session, or own category. It DOES legitimately
	// see every system default category (user_id IS NULL) — that's not a
	// leak, per ListCategoryChangesForUser's documented scoping (mirroring
	// the categories CRUD list's "(user_id = $1 OR user_id IS NULL)") — so
	// "categories" is checked separately for userA's specific row rather
	// than asserted empty like every other entity type.
	resp, err := svc.pull(ctx, userIDString(userB), "", 500)
	if err != nil {
		t.Fatalf("pull (userB): %v", err)
	}
	for entityType, cs := range resp.Changes {
		if entityType == "categories" {
			for _, u := range cs.Upserts {
				if u.(categoryUpsertDTO).ID == userACategoryID {
					t.Fatalf("userB's pull leaked userA's own category (cross-tenant leak)")
				}
			}
			continue
		}
		if len(cs.Upserts) != 0 {
			t.Fatalf("userB's pull returned %d upserts for %q, want 0 (cross-tenant leak)", len(cs.Upserts), entityType)
		}
	}
}

// insertUserCategory inserts a user-owned category directly via SQL
// (mirroring internal/categories/service_test.go's insertSystemCategory,
// but with user_id set rather than NULL) and returns its id string.
func insertUserCategory(t *testing.T, pool *pgxpool.Pool, user storedb.User, suffix string) string {
	t.Helper()
	ctx := context.Background()
	idStr := newUUIDv7(t)
	_, err := pool.Exec(ctx, `
		INSERT INTO categories (id, user_id, name, color, productivity, created_at, updated_at)
		VALUES ($1, $2, $3, '#000000', 0, now(), now())`,
		mustParseUUID(t, idStr), user.ID, fmt.Sprintf("pull-test-category-%s", suffix))
	if err != nil {
		t.Fatalf("insert user category: %v", err)
	}
	return idStr
}

// TestPullPagination proves multi-page keyset pagination behaves per
// documentation/sync-protocol.md §Pull: a first page bounded by limit
// reports has_more=true and, walking next_cursor until has_more is false,
// every inserted tag is eventually delivered exactly-at-least-once with no
// row skipped (beyond redelivery, which is documented-safe).
//
// RIZ-34 (pivot): unlike the pre-changelog design, which paginated each
// entity type independently so a page's "tags" upserts count could be
// asserted exactly against the requested limit, this implementation
// paginates a SINGLE feed (sync_changelog) shared across every entity
// type -- so page 1 with limit=2 can legitimately be consumed entirely by
// OTHER entity types' changelog rows that sort earlier in (xid8,
// server_seq) order (e.g. the system-default categories seeded once by
// migration 000026's backfill, which exist for every user and sort ahead
// of anything this test just inserted). This test therefore only asserts
// on the eventual, whole-walk outcome, not on any single page's
// per-entity-type composition -- exactly per this file's other
// multi-entity/categories pagination tests below.
func TestPullPagination(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	const total = 5
	suffix := randomSuffix(t)
	ids := make(map[string]bool, total)
	for i := 0; i < total; i++ {
		id := insertTag(t, pool, user, suffix, i)
		ids[id] = true
	}

	seen := map[string]bool{}
	cursor := ""
	hasMore := true
	pages := 0
	for hasMore {
		pages++
		if pages > total+20 {
			t.Fatalf("pull did not converge after %d pages (stuck cursor?)", pages)
		}
		page, err := svc.pull(ctx, userIDString(user), cursor, 2)
		if err != nil {
			t.Fatalf("pull (page %d): %v", pages, err)
		}
		for _, u := range page.Changes["tags"].Upserts {
			dto := u.(tagUpsertDTO)
			seen[dto.ID] = true
		}
		cursor = page.NextCursor
		hasMore = page.HasMore
	}
	if pages < 2 {
		t.Fatalf("pull converged in a single page, want at least 2 (5 tags + system categories exceed limit=2)")
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
	svc := &Service{Queries: q, Pool: pool}
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
	svc := &Service{Queries: q, Pool: pool}
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
		// "categories" is the one entity type a brand-new user can
		// legitimately see rows for with zero writes of their own: every
		// system default category (user_id IS NULL) is delivered to every
		// user per ListCategoryChangesForUser's scoping, so a client can
		// resolve category_id on any activity_event/user_app_setting it
		// pulls even before it has created a single category itself.
		if entityType == "categories" {
			continue
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
	svc := &Service{Queries: q, Pool: pool}
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
	svc := &Service{Queries: q, Pool: pool}
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

// TestPullMultiEntityPagination proves pagination across two entity types
// with different row counts (projects: 3, tags: 7) delivers every row
// exactly-at-least-once with no skip, and that next_cursor/has_more are
// computed correctly through the pages where the smaller entity type
// (projects) has already fully drained while the larger one (tags) still
// has rows pending — the "one type drains before the other" case M4
// calls out as untested.
func TestPullMultiEntityPagination(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	suffix := randomSuffix(t)
	const numProjects = 3
	const numTags = 7

	wantProjects := make(map[string]bool, numProjects)
	for i := 0; i < numProjects; i++ {
		wantProjects[newProject(t, pool, user)] = true
	}
	wantTags := make(map[string]bool, numTags)
	for i := 0; i < numTags; i++ {
		wantTags[insertTag(t, pool, user, suffix, i)] = true
	}

	seenProjects := map[string]bool{}
	seenTags := map[string]bool{}
	cursor := ""
	hasMore := true
	pages := 0
	for hasMore {
		pages++
		if pages > numProjects+numTags+2 {
			t.Fatalf("pull did not converge after %d pages (stuck cursor?)", pages)
		}

		resp, err := svc.pull(ctx, userIDString(user), cursor, 2)
		if err != nil {
			t.Fatalf("pull (page %d): %v", pages, err)
		}
		for _, u := range resp.Changes["projects"].Upserts {
			seenProjects[u.(projectUpsertDTO).ID] = true
		}
		for _, u := range resp.Changes["tags"].Upserts {
			seenTags[u.(tagUpsertDTO).ID] = true
		}
		cursor = resp.NextCursor
		hasMore = resp.HasMore
	}

	for id := range wantProjects {
		if !seenProjects[id] {
			t.Fatalf("project id %s was never delivered across any page", id)
		}
	}
	for id := range wantTags {
		if !seenTags[id] {
			t.Fatalf("tag id %s was never delivered across any page", id)
		}
	}
}

// TestPullTamperedCursorReturns400 proves a garbage/tampered cursor value
// is reported as an RFC 7807 400, never a 500, per M4's callout that this
// path lacked test coverage.
func TestPullTamperedCursorReturns400(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	h := NewHandler(svc)

	for _, tc := range []struct {
		name   string
		cursor string
	}{
		{"not base64 at all", "not-valid-base64!!!"},
		{"valid base64 but non-numeric payload", "Z2FyYmFnZQ"}, // base64url("garbage")
		{"valid base64, negative number", "LTU"},               // base64url("-5")
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/v1/sync/changes?cursor="+tc.cursor, nil)
			req = req.WithContext(auth.WithIdentity(req.Context(), auth.Identity{UserID: userIDString(user)}))
			rec := httptest.NewRecorder()

			h.PullChanges(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
			var problem struct {
				Status int `json:"status"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
				t.Fatalf("decode problem body %q: %v", rec.Body.String(), err)
			}
			if problem.Status != http.StatusBadRequest {
				t.Fatalf("problem.status = %d, want 400", problem.Status)
			}
		})
	}
}

// TestPullCategoriesPaginationAndTombstone proves categories (RIZ-34 M1)
// paginate and tombstone exactly like every other syncable entity: a
// user-owned category is delivered as an upsert, keyset-paginates across
// pages without skipping, and a subsequent soft-delete is delivered as a
// tombstone (never re-delivered as an upsert) once the cursor has advanced
// past the create.
func TestPullCategoriesPaginationAndTombstone(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	suffix := randomSuffix(t)
	const numCategories = 5
	wantIDs := make(map[string]bool, numCategories)
	for i := 0; i < numCategories; i++ {
		wantIDs[insertUserCategory(t, pool, user, fmt.Sprintf("%s-%d", suffix, i))] = true
	}

	seen := map[string]bool{}
	cursor := ""
	hasMore := true
	for hasMore {
		resp, err := svc.pull(ctx, userIDString(user), cursor, 2)
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		for _, u := range resp.Changes["categories"].Upserts {
			seen[u.(categoryUpsertDTO).ID] = true
		}
		cursor = resp.NextCursor
		hasMore = resp.HasMore
	}
	for id := range wantIDs {
		if !seen[id] {
			t.Fatalf("category id %s was never delivered as an upsert across any page", id)
		}
	}

	// Soft-delete one category and prove it's delivered as a tombstone
	// (never an upsert) on the next pull.
	var deletedID string
	for id := range wantIDs {
		deletedID = id
		break
	}
	if _, err := pool.Exec(ctx, `UPDATE categories SET deleted_at = now(), updated_at = now() WHERE id = $1`, mustParseUUID(t, deletedID)); err != nil {
		t.Fatalf("soft-delete category: %v", err)
	}

	final, err := svc.pull(ctx, userIDString(user), cursor, 500)
	if err != nil {
		t.Fatalf("pull (after delete): %v", err)
	}
	foundTombstone := false
	for _, ts := range final.Changes["categories"].Tombstones {
		if ts.(categoryTombstoneDTO).ID == deletedID {
			foundTombstone = true
		}
	}
	if !foundTombstone {
		t.Fatalf("deleted category was not delivered as a tombstone")
	}
	for _, u := range final.Changes["categories"].Upserts {
		if u.(categoryUpsertDTO).ID == deletedID {
			t.Fatalf("deleted category was delivered as an upsert, not a tombstone")
		}
	}
}

// TestPullXminHorizonExcludesRaceWithLowerServerSeq is the H1 regression
// test: it reproduces the scenario the fix targets — a transaction with a
// LOWER server_seq still uncommitted while a transaction with a HIGHER
// server_seq has already committed — and proves a pull neither delivers
// the higher-seq row in a way that would let next_cursor skip past the
// lower-seq one, nor ever permanently loses the lower-seq row once it
// commits.
//
// Ordering is deterministic via explicit BEGIN/COMMIT on two separate
// pooled connections (no sleeps): txLow's INSERT runs and is left
// uncommitted (so it is assigned a server_seq before txHigh's, via
// server_seq_global's ordering, but has not yet committed); txHigh's
// INSERT then runs to completion (auto-committed), guaranteeing txHigh's
// row has a HIGHER server_seq than txLow's while txLow is still open.
func TestPullXminHorizonExcludesRaceWithLowerServerSeq(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	txLow, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txLow: %v", err)
	}
	lowID := newUUIDv7(t)
	if _, err := txLow.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at) VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, lowID), user.ID, "concurrency-low-"+randomSuffix(t)); err != nil {
		t.Fatalf("insert low (uncommitted): %v", err)
	}

	// Committed immediately, on a different pooled connection, strictly
	// after txLow's INSERT has already been assigned its (lower)
	// server_seq — so this row's server_seq is guaranteed higher while
	// txLow is still uncommitted.
	highID := newUUIDv7(t)
	if _, err := pool.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at) VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, highID), user.ID, "concurrency-high-"+randomSuffix(t)); err != nil {
		t.Fatalf("insert high (committed): %v", err)
	}

	// While txLow is still open: the pull must not deliver "high" at all
	// (it's excluded by xid_before_snapshot_horizon since txLow, an
	// earlier-starting transaction with a lower server_seq, is still
	// in-flight), and obviously can't deliver "low" either (it isn't even
	// committed yet, so ordinary MVCC visibility already hides it). Either
	// way, next_cursor must not advance past a point that would cause
	// "low" to be skipped once it commits.
	duringRace, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (during race): %v", err)
	}
	for _, u := range duringRace.Changes["tags"].Upserts {
		if u.(tagUpsertDTO).ID == highID {
			t.Fatalf("pull delivered the higher-server_seq row while a lower-server_seq transaction was still uncommitted")
		}
		if u.(tagUpsertDTO).ID == lowID {
			t.Fatalf("pull delivered the uncommitted row (should be impossible via ordinary MVCC visibility alone)")
		}
	}

	if err := txLow.Commit(ctx); err != nil {
		t.Fatalf("commit txLow: %v", err)
	}

	// Re-pull from the cursor the racy pull returned: both rows must now
	// be delivered — "low" is not permanently skipped, and "high" (whether
	// or not it was already delivered above) is delivered at least once.
	afterCommit, err := svc.pull(ctx, userIDString(user), duringRace.NextCursor, 500)
	if err != nil {
		t.Fatalf("pull (after commit): %v", err)
	}
	seen := map[string]bool{}
	for _, u := range duringRace.Changes["tags"].Upserts {
		seen[u.(tagUpsertDTO).ID] = true
	}
	for _, u := range afterCommit.Changes["tags"].Upserts {
		seen[u.(tagUpsertDTO).ID] = true
	}
	if !seen[lowID] {
		t.Fatalf("the lower-server_seq row was never delivered even after its transaction committed (permanently skipped)")
	}
	if !seen[highID] {
		t.Fatalf("the higher-server_seq row was never delivered across either pull")
	}
}

// TestPullReversedInterleavingCommitOrderedCursor is the H1 RE-REVIEW
// regression test (RIZ-34): it reproduces the residual gap the original
// xmin-horizon-gate-plus-server_seq-cursor fix left open — a REVERSED
// interleaving where the COMMITTED row has the LOWER xid but the HIGHER
// server_seq, while the row still IN FLIGHT has the HIGHER xid but the
// LOWER server_seq (live repro this fix targets: committed seq=2061/
// xmin=4031 delivered, in-flight seq=2060/xmin=4032 skipped forever).
//
// This is the opposite shape from
// TestPullXminHorizonExcludesRaceWithLowerServerSeq above, which has the
// in-flight row assigned BOTH a lower xid AND a lower server_seq (the
// horizon gate alone already handles that ordinary case, since the
// in-flight transaction's own xid IS the snapshot horizon). Here, the
// in-flight transaction's xid is deliberately made HIGHER while its row's
// server_seq is made LOWER, which is exactly the shape where a
// server_seq-only cursor advances past the in-flight row's eventual
// position and permanently skips it once it commits — proving the fix
// requires anchoring the pagination key to the tuple (xid8, server_seq),
// not just gating delivery on xid8.
//
// Deterministic construction via two pooled connections with explicit
// BEGIN/COMMIT ordering (no sleeps):
//  1. txC (the eventually-committed transaction) BEGINs and calls
//     pg_current_xact_id(), which assigns it a real xid immediately (even
//     though it has not written anything yet) — this xid is the LOWEST of
//     the two because txC acquires it first.
//  2. txI (the row that stays in-flight through the race) BEGINs and also
//     calls pg_current_xact_id() — its xid is guaranteed HIGHER than
//     txC's, since xids are assigned monotonically and txC already has
//     one.
//  3. txI INSERTs its row FIRST (before txC inserts anything), so the
//     server_seq trigger's nextval() call assigns txI's row the LOWER
//     server_seq of the two — even though txI holds the HIGHER xid.
//  4. txC INSERTs its row SECOND (drawing a HIGHER server_seq than txI's
//     row) and COMMITs immediately, so it is fully settled with a LOWER
//     xid but a HIGHER server_seq than the still-open txI.
//  5. txI remains uncommitted through the assertions below, then commits.
func TestPullReversedInterleavingCommitOrderedCursor(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	txC, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txC: %v", err)
	}
	if _, err := txC.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		t.Fatalf("assign txC's xid: %v", err)
	}

	txI, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin txI: %v", err)
	}
	if _, err := txI.Exec(ctx, `SELECT pg_current_xact_id()`); err != nil {
		t.Fatalf("assign txI's xid: %v", err)
	}

	// txI inserts first: its row draws the LOWER server_seq despite txI
	// holding the HIGHER xid (assigned above, after txC's).
	inFlightID := newUUIDv7(t)
	if _, err := txI.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at) VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, inFlightID), user.ID, "reversed-inflight-"+randomSuffix(t)); err != nil {
		t.Fatalf("insert in-flight row: %v", err)
	}

	// txC inserts second (drawing a HIGHER server_seq than txI's row above)
	// and commits immediately: fully settled, LOWER xid, HIGHER server_seq
	// than the still-open txI.
	committedID := newUUIDv7(t)
	if _, err := txC.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at) VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, committedID), user.ID, "reversed-committed-"+randomSuffix(t)); err != nil {
		t.Fatalf("insert committed row: %v", err)
	}
	if err := txC.Commit(ctx); err != nil {
		t.Fatalf("commit txC: %v", err)
	}

	// While txI is still open: the committed row (server_seq=high,
	// xid=low) is delivered (its xid is below the snapshot horizon, which
	// sits at txI's xid since txI is the only in-flight transaction), and
	// the in-flight row must not be delivered at all. The critical
	// assertion is what happens AFTER txI commits (below): a server_seq-
	// only cursor computed from this page would be pinned at the
	// committed row's (higher) server_seq, permanently excluding txI's
	// (lower-server_seq) row once it commits.
	duringRace, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (during reversed race): %v", err)
	}
	foundCommitted := false
	for _, u := range duringRace.Changes["tags"].Upserts {
		dto := u.(tagUpsertDTO)
		if dto.ID == inFlightID {
			t.Fatalf("pull delivered the in-flight (uncommitted) row")
		}
		if dto.ID == committedID {
			foundCommitted = true
		}
	}
	if !foundCommitted {
		t.Fatalf("pull did not deliver the already-committed row (server_seq=high, xid=low)")
	}

	if err := txI.Commit(ctx); err != nil {
		t.Fatalf("commit txI: %v", err)
	}

	// Re-pull from the cursor the racy pull returned: the in-flight row
	// (now committed, server_seq=low, xid=high) must be delivered. This is
	// the assertion that fails against a server_seq-only cursor: the
	// in-flight row's server_seq is LOWER than the cursor boundary (the
	// already-delivered committed row's server_seq), so "server_seq >
	// cursor" would never admit it again. The (xid8, server_seq)-tuple
	// cursor admits it because its xid8 (the leading key) is higher than
	// the cursor's xid8 component, regardless of its server_seq.
	afterCommit, err := svc.pull(ctx, userIDString(user), duringRace.NextCursor, 500)
	if err != nil {
		t.Fatalf("pull (after commit): %v", err)
	}
	foundInFlight := false
	for _, u := range afterCommit.Changes["tags"].Upserts {
		if u.(tagUpsertDTO).ID == inFlightID {
			foundInFlight = true
		}
	}
	if !foundInFlight {
		t.Fatalf("the reversed-interleaving row (lower server_seq, higher xid) was permanently skipped once committed — next_cursor advanced past it")
	}
}

// TestPullActivityEventCompressedChunkRegression is the fatal-bug
// regression test motivating RIZ-34's pivot to sync_changelog (migration
// 000026): it forces the activity_events chunk holding a freshly-pushed
// event to compress (exactly what migration 000011's 30-day compression
// policy eventually does on its own), then proves a pull of that event
// still succeeds and delivers it.
//
// This test MUST FAIL against the pre-pivot design (migrations
// 000024/000025), which paginated activity_events directly by
// `xmin_xid8(ae.xmin)` — TimescaleDB rejects any system-column reference
// other than tableoid against a compressed chunk
// ("transparent decompression only supports tableoid system column"), so
// that query 500s permanently once the chunk is compressed. Post-pivot,
// pagination lives entirely on sync_changelog (a plain, never-hypertable,
// never-compressed table), and the only query that still touches
// activity_events directly (GetActivityEventForChangelogEntry) reads only
// ordinary columns — no system-column reference at all — which
// TimescaleDB permits unconditionally against a compressed chunk.
func TestPullActivityEventCompressedChunkRegression(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	eventID := newUUIDv7(t)
	suffix := randomSuffix(t)
	startedAt := time.Now().UTC().Truncate(time.Second)
	endedAt := startedAt.Add(5 * time.Minute)

	pushResp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItem(t, eventID, "com.example.compressed."+suffix, startedAt, endedAt)},
	})
	if err != nil {
		t.Fatalf("push (fixture): %v", err)
	}
	if pushResp.Results[0].Status != "applied" {
		t.Fatalf("push (fixture) status = %q, want applied", pushResp.Results[0].Status)
	}

	// TimescaleDB refuses to compress ANY chunk of a hypertable that has a
	// foreign key pointing INTO it (compress_chunk: "found a FK into a
	// chunk while truncating") — event_tags' composite FK to
	// activity_events (migration 000013) is exactly that, even though no
	// code in this repo populates event_tags yet. Drop it for the duration
	// of this test (restored via t.Cleanup) so compress_chunk below can
	// run; this mirrors the real 30-day compression policy eventually
	// hitting this exact restriction in production once event_tags rows
	// exist, which is a pre-existing, separate concern from this ticket's
	// pull-pagination fix and out of scope to resolve here.
	if _, err := pool.Exec(ctx, `ALTER TABLE event_tags DROP CONSTRAINT event_tags_activity_event_fk`); err != nil {
		t.Fatalf("drop event_tags FK: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			ALTER TABLE event_tags ADD CONSTRAINT event_tags_activity_event_fk
				FOREIGN KEY (user_id, started_at, entity_id)
				REFERENCES activity_events (user_id, started_at, event_id)`)
	})

	// Force every activity_events chunk to compress. if_not_compressed=true
	// makes this safe to call even though other tests' chunks (same
	// 7-day chunk interval) may already be compressed from a prior run.
	if _, err := pool.Exec(ctx, `SELECT compress_chunk(c, if_not_compressed => true) FROM show_chunks('activity_events') c`); err != nil {
		t.Fatalf("compress_chunk: %v", err)
	}

	resp, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull after chunk compression: %v (must succeed — see migration 000026's header comment)", err)
	}
	found := false
	for _, u := range resp.Changes["activity_events"].Upserts {
		if u.(activityEventUpsertDTO).EventID == eventID {
			found = true
		}
	}
	if !found {
		t.Fatalf("pull did not deliver the activity_event after its hypertable chunk was compressed")
	}
}

// TestPullActivityEventTombstoneDeliveryViaChangelog proves an
// activity_event tombstone push is delivered by the sync_changelog-backed
// pull as a tombstone (never re-delivered as an upsert), closing the
// coverage gap the pre-pivot design's per-entity-type test suite never
// exercised for activity_events specifically (TestPullTombstoneDelivery
// above only covers projects).
func TestPullActivityEventTombstoneDeliveryViaChangelog(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	eventID := newUUIDv7(t)
	suffix := randomSuffix(t)
	startedAt := time.Now().UTC().Truncate(time.Second)
	endedAt := startedAt.Add(5 * time.Minute)
	bundleID := "com.example.changelog." + suffix

	if _, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), false)},
	}); err != nil {
		t.Fatalf("push (create): %v", err)
	}

	first, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (before tombstone): %v", err)
	}
	foundUpsert := false
	for _, u := range first.Changes["activity_events"].Upserts {
		if u.(activityEventUpsertDTO).EventID == eventID {
			foundUpsert = true
		}
	}
	if !foundUpsert {
		t.Fatalf("first pull did not include the created activity_event as an upsert")
	}

	if _, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), true)},
	}); err != nil {
		t.Fatalf("push (tombstone): %v", err)
	}

	second, err := svc.pull(ctx, userIDString(user), first.NextCursor, 500)
	if err != nil {
		t.Fatalf("pull (after tombstone): %v", err)
	}
	foundTombstone := false
	for _, ts := range second.Changes["activity_events"].Tombstones {
		if ts.(activityEventTombstoneDTO).EventID == eventID {
			foundTombstone = true
		}
	}
	if !foundTombstone {
		t.Fatalf("second pull did not deliver the activity_event tombstone")
	}
	for _, u := range second.Changes["activity_events"].Upserts {
		if u.(activityEventUpsertDTO).EventID == eventID {
			t.Fatalf("second pull delivered the tombstoned activity_event as an upsert, not a tombstone")
		}
	}
}

// TestPullDedupeWithinPageKeepsLatestState proves that when the same
// entity is written to twice before a pull ever observes it (so both
// writes' sync_changelog rows land in the SAME page), the pull delivers
// exactly ONE upsert for that entity — reflecting its LATEST state, not
// the stale intermediate one — rather than two upserts or the first
// write's now-superseded values. This is the "dedupe-within-page" behavior
// internal/sync/pull.go's doc comment describes.
func TestPullDedupeWithinPageKeepsLatestState(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	suffix := randomSuffix(t)
	tagID := insertTag(t, pool, user, suffix, 0)

	// Two updates before any pull observes the tag: both produce a
	// sync_changelog row (migration 000026's tags_log_change trigger fires
	// on every UPDATE), so both land in the same page below.
	if _, err := pool.Exec(ctx, `UPDATE tags SET name = $2, updated_at = now() WHERE id = $1`,
		mustParseUUID(t, tagID), "dedupe-first-"+suffix); err != nil {
		t.Fatalf("first update: %v", err)
	}
	finalName := "dedupe-second-" + suffix
	if _, err := pool.Exec(ctx, `UPDATE tags SET name = $2, updated_at = now() WHERE id = $1`,
		mustParseUUID(t, tagID), finalName); err != nil {
		t.Fatalf("second update: %v", err)
	}

	resp, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	matches := 0
	var lastName string
	for _, u := range resp.Changes["tags"].Upserts {
		dto := u.(tagUpsertDTO)
		if dto.ID == tagID {
			matches++
			lastName = dto.Name
		}
	}
	if matches != 1 {
		t.Fatalf("tag id %s appeared %d times in one page's upserts, want exactly 1 (dedupe-within-page)", tagID, matches)
	}
	if lastName != finalName {
		t.Fatalf("deduped upsert's name = %q, want the LATEST write's name %q", lastName, finalName)
	}
}

// TestPullBackfillDeliversPreExistingRows proves migration 000026's
// backfill mechanism: a syncable row that existed BEFORE its
// sync_changelog entry was ever recorded (simulating a row written before
// this migration ran, when no *_log_change trigger existed yet to record
// it automatically) is still discoverable by a fresh cursor once a
// changelog row is backfilled for it — exactly the
// `INSERT INTO sync_changelog (...) SELECT ... FROM tags` statement
// migration 000026 runs once, for every pre-existing row, at migration
// time.
//
// This test reproduces that scenario directly rather than re-running the
// migration from scratch (which already ran once against this database
// and cannot be meaningfully re-applied to prove the point): it inserts a
// tag with the tags_log_change trigger disabled (so the row exists with NO
// changelog entry, exactly like a pre-migration row before 000026 backfilled
// it), then runs the identical backfill INSERT migration 000026 uses,
// scoped to just this one row, and proves a fresh-cursor pull discovers it.
func TestPullBackfillDeliversPreExistingRows(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, _ := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	suffix := randomSuffix(t)
	tagID := newUUIDv7(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `ALTER TABLE tags DISABLE TRIGGER tags_log_change`); err != nil {
		t.Fatalf("disable trigger: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at) VALUES ($1, $2, $3, now())`,
		mustParseUUID(t, tagID), user.ID, "backfill-pre-existing-"+suffix); err != nil {
		t.Fatalf("insert tag (trigger disabled): %v", err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE tags ENABLE TRIGGER tags_log_change`); err != nil {
		t.Fatalf("re-enable trigger: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Sanity check: with no changelog entry at all, the row is invisible to
	// a fresh pull.
	before, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (before backfill): %v", err)
	}
	for _, u := range before.Changes["tags"].Upserts {
		if u.(tagUpsertDTO).ID == tagID {
			t.Fatalf("tag was visible before any changelog row was backfilled for it (test setup is broken)")
		}
	}

	// The exact backfill statement migration 000026 runs for every
	// pre-existing tags row, scoped here to just this one.
	if _, err := pool.Exec(ctx, `
		INSERT INTO sync_changelog (user_id, entity_type, entity_id, server_seq)
		SELECT user_id, 'tags', id, server_seq FROM tags WHERE id = $1`, mustParseUUID(t, tagID)); err != nil {
		t.Fatalf("backfill insert: %v", err)
	}

	after, err := svc.pull(ctx, userIDString(user), "", 500)
	if err != nil {
		t.Fatalf("pull (after backfill): %v", err)
	}
	found := false
	for _, u := range after.Changes["tags"].Upserts {
		if u.(tagUpsertDTO).ID == tagID {
			found = true
		}
	}
	if !found {
		t.Fatalf("backfilled tag was not delivered to a fresh cursor")
	}
}
