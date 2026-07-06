package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// TestServerSeqTriggerUpdateWithoutExplicitValue is RIZ-33's required proof
// that the BEFORE INSERT OR UPDATE trigger introduced by migration 000022
// (server_seq_triggers) — rather than the per-column
// `DEFAULT nextval('server_seq_global')` from 000021, which only fired on
// INSERT — assigns a fresh server_seq on an UPDATE that does not mention
// server_seq at all. Every value used here is generated fresh inside the
// test body (not a package-level var) so the test is safe to run
// repeatedly in the same process under `go test -count=2`.
func TestServerSeqTriggerUpdateWithoutExplicitValue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)

	suffix := randomHex(8)
	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("trigger-user+%s@example.com", suffix)),
		DisplayName: textPtr("Trigger Test User"),
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
		Name:       "trigger-test-device",
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

	// focus_sessions: insert without specifying server_seq at all (no
	// column in the INSERT's column list), then run a raw UPDATE that
	// likewise never mentions server_seq, and assert it still comes back
	// strictly greater — proof the trigger (not a caller-supplied value or
	// a column DEFAULT, which never fires on UPDATE) is what bumps it.
	sessionID := newUUIDv4(t, pool)
	var insertedSeq int64
	err = pool.QueryRow(ctx, `
		INSERT INTO focus_sessions (
			id, user_id, device_id, kind, started_at, status, created_at, updated_at
		) VALUES (
			$1, $2, $3, 'focus', now(), 'running', now(), now()
		)
		RETURNING server_seq`,
		sessionID, user.ID, device.ID,
	).Scan(&insertedSeq)
	if err != nil {
		t.Fatalf("insert focus_sessions: %v", err)
	}
	if insertedSeq <= 0 {
		t.Fatalf("insertedSeq = %d, want a positive value assigned by the trigger", insertedSeq)
	}

	var updatedSeq int64
	err = pool.QueryRow(ctx, `
		UPDATE focus_sessions
		SET status = 'completed', ended_at = now(), updated_at = now()
		WHERE id = $1
		RETURNING server_seq`,
		sessionID,
	).Scan(&updatedSeq)
	if err != nil {
		t.Fatalf("update focus_sessions: %v", err)
	}
	if updatedSeq <= insertedSeq {
		t.Fatalf("updatedSeq (%d) should be strictly greater than insertedSeq (%d): the trigger must assign a fresh value on UPDATE even though the UPDATE statement never mentions server_seq", updatedSeq, insertedSeq)
	}
}

// TestServerSeqTriggerAllSevenTablesShareOneSequence is RIZ-33's required
// proof that cross-table monotonic assignment holds across every one of
// the seven syncable tables (users, categories, user_app_settings,
// projects, tags, activity_events, focus_sessions), not just the two
// covered by the pre-existing TestGlobalServerSeqSequence (users,
// categories). Each write's server_seq is asserted strictly greater than
// the one immediately before it, proving all seven triggers draw from the
// same server_seq_global sequence in write order. All test data is
// generated fresh per invocation.
func TestServerSeqTriggerAllSevenTablesShareOneSequence(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := storedb.New(pool)
	suffix := randomHex(8)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{ // 1: users
		Email:       textPtr(fmt.Sprintf("seven-tables+%s@example.com", suffix)),
		DisplayName: textPtr("Seven Tables"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(ctx, user.ID) })
	prev := user.ServerSeq

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID: user.ID, Platform: "macos", Name: "seven-tables-device",
		Model: "m", OsVersion: "1.0", AppVersion: "1.0",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}
	t.Cleanup(func() { _ = q.RevokeDevice(ctx, storedb.RevokeDeviceParams{ID: device.ID, UserID: user.ID}) })

	var categorySeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO categories (id, user_id, name, color, productivity, created_at, updated_at)
		VALUES (gen_random_uuid(), $1, $2, '#123456', 1, now(), now())
		RETURNING server_seq`,
		user.ID, "Category "+suffix,
	).Scan(&categorySeq); err != nil { // 2: categories
		t.Fatalf("insert categories: %v", err)
	}
	assertIncreasing(t, "categories", prev, categorySeq)
	prev = categorySeq

	appID := newUUIDv4(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO apps (id, bundle_id, platform, name) VALUES ($1, $2, 'macos', 'Test App')`,
		appID, fmt.Sprintf("com.example.seven-tables.%s", suffix),
	); err != nil {
		t.Fatalf("insert apps: %v", err)
	}

	var uasSeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO user_app_settings (user_id, app_id, excluded, updated_at)
		VALUES ($1, $2, false, now())
		RETURNING server_seq`,
		user.ID, appID,
	).Scan(&uasSeq); err != nil { // 3: user_app_settings
		t.Fatalf("insert user_app_settings: %v", err)
	}
	assertIncreasing(t, "user_app_settings", prev, uasSeq)
	prev = uasSeq

	projectID := newUUIDv4(t, pool)
	var projectSeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO projects (id, user_id, name, color, created_at, updated_at)
		VALUES ($1, $2, $3, '#abcdef', now(), now())
		RETURNING server_seq`,
		projectID, user.ID, "Project "+suffix,
	).Scan(&projectSeq); err != nil { // 4: projects
		t.Fatalf("insert projects: %v", err)
	}
	assertIncreasing(t, "projects", prev, projectSeq)
	prev = projectSeq

	var tagSeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO tags (id, user_id, name, updated_at)
		VALUES (gen_random_uuid(), $1, $2, now())
		RETURNING server_seq`,
		user.ID, "Tag "+suffix,
	).Scan(&tagSeq); err != nil { // 5: tags
		t.Fatalf("insert tags: %v", err)
	}
	assertIncreasing(t, "tags", prev, tagSeq)
	prev = tagSeq

	eventID := newUUIDv4(t, pool)
	startedAt := time.Now().Truncate(time.Second)
	var activitySeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO activity_events (
			event_id, user_id, device_id, started_at, ended_at, type, source, inserted_at
		) VALUES (
			$1, $2, $3, $4, $5, 'app_active', 'desktop', now()
		)
		RETURNING server_seq`,
		eventID, user.ID, device.ID, startedAt, startedAt.Add(time.Minute),
	).Scan(&activitySeq); err != nil { // 6: activity_events
		t.Fatalf("insert activity_events: %v", err)
	}
	assertIncreasing(t, "activity_events", prev, activitySeq)
	prev = activitySeq

	sessionID := newUUIDv4(t, pool)
	var focusSeq int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO focus_sessions (id, user_id, device_id, kind, started_at, status, created_at, updated_at)
		VALUES ($1, $2, $3, 'focus', now(), 'running', now(), now())
		RETURNING server_seq`,
		sessionID, user.ID, device.ID,
	).Scan(&focusSeq); err != nil { // 7: focus_sessions
		t.Fatalf("insert focus_sessions: %v", err)
	}
	assertIncreasing(t, "focus_sessions", prev, focusSeq)
}

func assertIncreasing(t *testing.T, table string, prev, got int64) {
	t.Helper()
	if got <= prev {
		t.Fatalf("%s.server_seq (%d) should be strictly greater than the immediately preceding write's server_seq (%d): all seven syncable tables must draw from the same global sequence", table, got, prev)
	}
}
