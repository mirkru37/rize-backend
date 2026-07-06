package sync

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// randomSuffix returns a fresh random hex string on every call (as
// opposed to a package-level var computed once per process), so tests in
// this file are safe to run repeatedly within the same process under
// `go test -count=2`.
func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping sync integration test")
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

// newUser creates a fresh user + macos device for a test, scoped with a
// per-call random suffix so it never collides with a prior/parallel
// invocation of the same test under -count=2.
func newUser(t *testing.T, q *storedb.Queries) (storedb.User, storedb.Device) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("sync-test+%s@example.com", suffix)),
		DisplayName: textPtr("Sync Test User"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(ctx, user.ID) })

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID: user.ID, Platform: "macos", Name: "sync-test-device",
		Model: "test-model", OsVersion: "1.0", AppVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	t.Cleanup(func() { _ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID}) })

	return user, device
}

func activityEventItem(t *testing.T, eventID, bundleID string, startedAt, endedAt time.Time) pushItem {
	t.Helper()
	return activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), false)
}

// activityEventItemFull builds an activity_event pushItem with full control
// over `type` (nil to omit it from the wire payload, exercising the
// optional-type default) and `deleted` (for tombstone-push tests).
func activityEventItemFull(t *testing.T, eventID, bundleID string, startedAt, endedAt time.Time, eventType *string, deleted bool) pushItem {
	t.Helper()
	data, err := json.Marshal(activityEventData{
		EventID:     eventID,
		StartedAt:   startedAt.Format(timeLayout),
		EndedAt:     endedAt.Format(timeLayout),
		AppBundleID: bundleID,
		Precision:   "exact",
		Type:        eventType,
		Deleted:     deleted,
	})
	if err != nil {
		t.Fatalf("marshal activityEventData: %v", err)
	}
	return pushItem{EntityType: "activity_event", Data: data}
}

func focusSessionItem(t *testing.T, id string, updatedAt, startedAt time.Time) pushItem {
	t.Helper()
	data, err := json.Marshal(focusSessionData{
		ID:        id,
		UpdatedAt: updatedAt.Format(timeLayout),
		StartedAt: startedAt.Format(timeLayout),
		Kind:      "focus",
		Status:    "running",
		Note:      textPtr("test session"),
	})
	if err != nil {
		t.Fatalf("marshal focusSessionData: %v", err)
	}
	return pushItem{EntityType: "focus_session", Data: data}
}

func userIDString(user storedb.User) string { return user.ID.String() }

// newUUIDv7 generates a fresh random UUID string for use as a
// client-supplied id in tests. It doesn't need to be a "real" RFC 9562
// UUIDv7 (time-ordering isn't asserted by anything in this test file) —
// only a well-formed, unique UUID, which parseUUID accepts regardless of
// version.
func newUUIDv7(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x70 // version 7
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// mustParseUUID parses a canonical UUID string into a pgtype.UUID, failing
// the test on error. Used by tests that need to query a row back by an id
// they generated as a plain string via newUUIDv7.
func mustParseUUID(t *testing.T, s string) pgtype.UUID {
	t.Helper()
	id, err := parseUUID(s)
	if err != nil {
		t.Fatalf("parseUUID(%q): %v", s, err)
	}
	return id
}

// TestPushIdempotentRetry proves that posting the same activity_event
// batch twice produces no duplicate rows: the first push reports
// "applied", the second (identical) push reports "duplicate" for the same
// item.
func TestPushIdempotentRetry(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	eventID := newUUIDv7(t)
	startedAt := time.Now().UTC().Truncate(time.Second)
	req := pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItem(t, eventID, "com.example.idempotent."+suffix, startedAt, startedAt.Add(5*time.Minute))},
	}

	first, err := svc.push(ctx, userIDString(user), req)
	if err != nil {
		t.Fatalf("push (first): %v", err)
	}
	if len(first.Results) != 1 || first.Results[0].Status != "applied" {
		t.Fatalf("first push results = %+v, want a single applied result", first.Results)
	}

	second, err := svc.push(ctx, userIDString(user), req)
	if err != nil {
		t.Fatalf("push (second, retry): %v", err)
	}
	if len(second.Results) != 1 || second.Results[0].Status != "duplicate" {
		t.Fatalf("second push results = %+v, want a single duplicate result", second.Results)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE user_id = $1 AND event_id = $2`, user.ID, mustParseUUID(t, eventID)).Scan(&count); err != nil {
		t.Fatalf("count activity_events: %v", err)
	}
	if count != 1 {
		t.Fatalf("activity_events row count = %d, want exactly 1 (no duplicate row created by the retry)", count)
	}
}

// TestPushPartialFailureBatch proves that a batch mixing a valid and an
// invalid item is processed item-by-item: the valid item is applied, the
// invalid one is reported as "invalid" with a validation error, and the
// valid item's row is still committed.
func TestPushPartialFailureBatch(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	validEventID := newUUIDv7(t)
	startedAt := time.Now().UTC().Truncate(time.Second)

	req := pushRequest{
		DeviceID: device.ID.String(),
		Items: []pushItem{
			activityEventItem(t, validEventID, "com.example.partial-valid."+suffix, startedAt, startedAt.Add(2*time.Minute)),
			// invalid: ended_at precedes started_at.
			activityEventItem(t, newUUIDv7(t), "com.example.partial-invalid."+suffix, startedAt, startedAt.Add(-time.Minute)),
		},
	}

	resp, err := svc.push(ctx, userIDString(user), req)
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Status != "applied" {
		t.Errorf("results[0].Status = %q, want applied", resp.Results[0].Status)
	}
	if resp.Results[1].Status != "invalid" {
		t.Errorf("results[1].Status = %q, want invalid", resp.Results[1].Status)
	}
	if resp.Results[1].Error == nil || resp.Results[1].Error.Code != "VALIDATION_ERROR" {
		t.Errorf("results[1].Error = %+v, want a VALIDATION_ERROR", resp.Results[1].Error)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM activity_events WHERE user_id = $1 AND event_id = $2`, user.ID, mustParseUUID(t, validEventID)).Scan(&count); err != nil {
		t.Fatalf("count activity_events: %v", err)
	}
	if count != 1 {
		t.Fatalf("valid item's activity_events row count = %d, want 1 (the invalid sibling item must not block it)", count)
	}
}

// TestPushFocusSessionLWWConflict proves that an older focus_session
// update loses to a newer one already stored: pushing an update with an
// updated_at earlier than what's already persisted is reported as
// "duplicate" and does not overwrite the stored row.
func TestPushFocusSessionLWWConflict(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	sessionID := newUUIDv7(t)
	base := time.Now().UTC().Truncate(time.Second)

	// First write: applied, establishes the stored updated_at = base+10m.
	first, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{focusSessionItem(t, sessionID, base.Add(10*time.Minute), base)},
	})
	if err != nil {
		t.Fatalf("push (first, newer): %v", err)
	}
	if first.Results[0].Status != "applied" {
		t.Fatalf("first push status = %q, want applied", first.Results[0].Status)
	}
	firstSeq := first.Results[0].ServerSeq
	if firstSeq == nil {
		t.Fatal("first push result missing server_seq")
	}

	// Second write: an OLDER updated_at than what's already stored must
	// lose under last-write-wins and be reported as "duplicate".
	second, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{focusSessionItem(t, sessionID, base.Add(time.Minute), base)},
	})
	if err != nil {
		t.Fatalf("push (second, older): %v", err)
	}
	if second.Results[0].Status != "duplicate" {
		t.Fatalf("second (older) push status = %q, want duplicate", second.Results[0].Status)
	}

	var storedUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM focus_sessions WHERE id = $1`, mustParseUUID(t, sessionID)).Scan(&storedUpdatedAt); err != nil {
		t.Fatalf("select focus_sessions: %v", err)
	}
	if !storedUpdatedAt.Equal(base.Add(10 * time.Minute)) {
		t.Errorf("stored updated_at = %v, want the first (newer) write's %v to still be in place", storedUpdatedAt, base.Add(10*time.Minute))
	}
}

// TestPushOversizeBatchRejected proves a batch of more than 500 items is
// rejected per documentation/sync-protocol.md §Push.
func TestPushOversizeBatchRejected(t *testing.T) {
	q := storedb.New(testPool(t))
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	items := make([]pushItem, 501)
	base := time.Now().UTC().Truncate(time.Second)
	for i := range items {
		items[i] = activityEventItem(t, newUUIDv7(t), "com.example.oversize", base, base.Add(time.Minute))
	}

	_, err := svc.push(ctx, userIDString(user), pushRequest{DeviceID: device.ID.String(), Items: items})
	if err == nil {
		t.Fatal("expected ErrBatchTooLarge for a 501-item batch, got nil")
	}
	if err != ErrBatchTooLarge { //nolint:errorlint // sentinel comparison mirrors the package's other exact-sentinel checks
		t.Fatalf("err = %v, want ErrBatchTooLarge", err)
	}
}

// TestPushTenantIsolation proves that a device_id belonging to a different
// user cannot be used to push events, even though the request body claims
// it: user_id is taken only from the authenticated context (userIDString
// here stands in for the access token's sub claim), never trusted from the
// request body.
func TestPushTenantIsolation(t *testing.T) {
	q := storedb.New(testPool(t))
	userA, _ := newUser(t, q)
	_, deviceB := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	_, err := svc.push(ctx, userIDString(userA), pushRequest{
		DeviceID: deviceB.ID.String(),
		Items:    []pushItem{activityEventItem(t, newUUIDv7(t), "com.example.tenant-isolation", base, base.Add(time.Minute))},
	})
	if err == nil {
		t.Fatal("expected ErrDeviceNotFound when user A pushes against user B's device_id, got nil")
	}
	if err != ErrDeviceNotFound { //nolint:errorlint // sentinel comparison mirrors the package's other exact-sentinel checks
		t.Fatalf("err = %v, want ErrDeviceNotFound", err)
	}
}

// TestPushClockSkewSubstitutesServerTime proves that an updated_at more
// than 24h off from server time on a focus_session write is replaced by
// server time before being persisted, per documentation/sync-protocol.md
// §Edge Cases §Clock Skew.
func TestPushClockSkewSubstitutesServerTime(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)

	fixedNow := time.Now().UTC().Truncate(time.Second)
	svc := &Service{Queries: q, now: func() time.Time { return fixedNow }}
	ctx := context.Background()

	sessionID := newUUIDv7(t)
	skewedUpdatedAt := fixedNow.Add(-48 * time.Hour)

	resp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{focusSessionItem(t, sessionID, skewedUpdatedAt, fixedNow.Add(-time.Hour))},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if resp.Results[0].Status != "applied" {
		t.Fatalf("status = %q, want applied", resp.Results[0].Status)
	}

	var storedUpdatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT updated_at FROM focus_sessions WHERE id = $1`, mustParseUUID(t, sessionID)).Scan(&storedUpdatedAt); err != nil {
		t.Fatalf("select focus_sessions: %v", err)
	}
	if !storedUpdatedAt.Equal(fixedNow) {
		t.Errorf("stored updated_at = %v, want server time %v substituted for the skewed client value", storedUpdatedAt, fixedNow)
	}
}

// TestPushActivityEventTypeOmittedDefaultsToManual proves that an
// activity_event item that omits `type` entirely (a doc-conformant client,
// per documentation/sync-protocol.md's worked example) is still accepted
// and applied, per RIZ-33 code-review fix (HIGH-3): the server defaults
// the NOT NULL `type` column to "manual" rather than rejecting the item.
func TestPushActivityEventTypeOmittedDefaultsToManual(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	eventID := newUUIDv7(t)
	startedAt := time.Now().UTC().Truncate(time.Second)

	resp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, "com.example.type-omitted."+suffix, startedAt, startedAt.Add(time.Minute), nil, false)},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Status != "applied" {
		t.Fatalf("results = %+v, want a single applied result", resp.Results)
	}

	var storedType string
	if err := pool.QueryRow(ctx, `SELECT type FROM activity_events WHERE user_id = $1 AND event_id = $2`, user.ID, mustParseUUID(t, eventID)).Scan(&storedType); err != nil {
		t.Fatalf("select activity_events: %v", err)
	}
	if storedType != "manual" {
		t.Errorf("stored type = %q, want default %q", storedType, "manual")
	}
}

// TestPushActivityEventInvalidTypeRejected proves that an
// explicitly-supplied `type` outside the documented enum is still rejected
// as "invalid" (the optional-type change only relaxes omission, not the
// enum check on a supplied value).
func TestPushActivityEventInvalidTypeRejected(t *testing.T) {
	q := storedb.New(testPool(t))
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	startedAt := time.Now().UTC().Truncate(time.Second)
	resp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, newUUIDv7(t), "com.example.type-invalid", startedAt, startedAt.Add(time.Minute), textPtr("not-a-real-type"), false)},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Status != "invalid" {
		t.Fatalf("results = %+v, want a single invalid result", resp.Results)
	}
	if resp.Results[0].Error == nil || resp.Results[0].Error.Code != "VALIDATION_ERROR" {
		t.Errorf("results[0].Error = %+v, want a VALIDATION_ERROR", resp.Results[0].Error)
	}
}

// TestPushActivityEventTombstone proves that a create followed by a
// tombstone push (same event_id/started_at, deleted: true) flips the
// stored row's `deleted` flag to true without changing any other column,
// and is reported "applied" — not silently dropped as "duplicate" — per
// RIZ-33 code-review fix (HIGH-4).
func TestPushActivityEventTombstone(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	suffix := randomSuffix(t)
	eventID := newUUIDv7(t)
	startedAt := time.Now().UTC().Truncate(time.Second)
	endedAt := startedAt.Add(5 * time.Minute)
	bundleID := "com.example.tombstone." + suffix

	create, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), false)},
	})
	if err != nil {
		t.Fatalf("push (create): %v", err)
	}
	if create.Results[0].Status != "applied" {
		t.Fatalf("create status = %q, want applied", create.Results[0].Status)
	}

	tombstone, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), true)},
	})
	if err != nil {
		t.Fatalf("push (tombstone): %v", err)
	}
	if tombstone.Results[0].Status != "applied" {
		t.Fatalf("tombstone status = %q, want applied", tombstone.Results[0].Status)
	}

	var (
		deleted                      bool
		storedType, storedWindow     sql.NullString
		storedStartedAt, storedEnded time.Time
	)
	err = pool.QueryRow(ctx, `SELECT deleted, type, window_title, started_at, ended_at FROM activity_events WHERE user_id = $1 AND event_id = $2`,
		user.ID, mustParseUUID(t, eventID)).Scan(&deleted, &storedType, &storedWindow, &storedStartedAt, &storedEnded)
	if err != nil {
		t.Fatalf("select activity_events: %v", err)
	}
	if !deleted {
		t.Error("stored deleted = false, want true after tombstone push")
	}
	if storedType.String != "app_active" {
		t.Errorf("stored type = %q, want unchanged %q", storedType.String, "app_active")
	}
	if !storedStartedAt.Equal(startedAt) || !storedEnded.Equal(endedAt) {
		t.Errorf("stored started_at/ended_at = %v/%v, want unchanged %v/%v", storedStartedAt, storedEnded, startedAt, endedAt)
	}

	// Re-pushing the same tombstone a second time must be a no-op,
	// reported as "duplicate".
	again, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItemFull(t, eventID, bundleID, startedAt, endedAt, textPtr("app_active"), true)},
	})
	if err != nil {
		t.Fatalf("push (re-tombstone): %v", err)
	}
	if again.Results[0].Status != "duplicate" {
		t.Fatalf("re-tombstone status = %q, want duplicate", again.Results[0].Status)
	}
}
