package sync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// TestPullFocusSessionIncludesProjectID pushes a focus session associated
// with a project, then pulls it back, asserting project_id is present on
// the wire (exercising formatOptionalUUID's non-nil branch in pull.go,
// which is otherwise never reached since every other pull test in this
// package uses project-less focus sessions).
func TestPullFocusSessionIncludesProjectID(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q, Pool: pool}
	ctx := context.Background()

	projectID := newProject(t, pool, user)
	base := time.Now().UTC().Truncate(time.Second)
	sessionID := newUUIDv7(t)

	pushResp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{focusSessionItemWithProject(t, sessionID, base, base, projectID)},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(pushResp.Results) != 1 || pushResp.Results[0].Status != "applied" {
		t.Fatalf("push results = %+v, want a single applied result", pushResp.Results)
	}

	pullResp, err := svc.pull(ctx, userIDString(user), "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	found := false
	for _, u := range pullResp.Changes["focus_sessions"].Upserts {
		dto, ok := u.(focusSessionUpsertDTO)
		if !ok {
			t.Fatalf("focus session upsert is not a focusSessionUpsertDTO: %T", u)
		}
		if dto.ID != sessionID {
			continue
		}
		found = true
		if dto.ProjectID == nil || *dto.ProjectID != projectID {
			t.Errorf("focus session %s project_id = %v, want %q", sessionID, dto.ProjectID, projectID)
		}
	}
	if !found {
		t.Fatalf("pulled focus sessions did not include %s: %+v", sessionID, pullResp.Changes["focus_sessions"].Upserts)
	}
}

// TestPushUnsupportedEntityTypeReportedInvalid asserts an item with an
// entity_type this endpoint doesn't implement is reported as a per-item
// "invalid" result (exercising invalidUnsupportedEntity) rather than
// aborting the whole batch, per documentation/sync-protocol.md §Push's
// "Partial success is allowed."
func TestPushUnsupportedEntityTypeReportedInvalid(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	resp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items: []pushItem{
			{EntityType: "not_a_real_entity_type", Data: []byte(`{}`)},
		},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %+v, want exactly one result", resp.Results)
	}
	result := resp.Results[0]
	if result.Status != "invalid" {
		t.Errorf("status = %q, want invalid", result.Status)
	}
	if result.EntityType != "not_a_real_entity_type" {
		t.Errorf("entity_type = %q, want the unsupported type echoed back", result.EntityType)
	}
	if result.Error == nil || result.Error.Code != "UNSUPPORTED_ENTITY_TYPE" {
		t.Errorf("error = %+v, want code UNSUPPORTED_ENTITY_TYPE", result.Error)
	}
}

// TestPushFocusSessionCrossUserIDCollisionForbidden exercises
// applyFocusSession's FORBIDDEN branch: pushing a focus_session id that's
// already owned by a DIFFERENT user reports "invalid"/FORBIDDEN rather
// than silently overwriting or reporting a misleading "duplicate", per
// documentation/security.md §Tenant Isolation.
func TestPushFocusSessionCrossUserIDCollisionForbidden(t *testing.T) {
	q := storedb.New(testPool(t))
	userA, deviceA := newUser(t, q)
	userB, deviceB := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()

	sessionID := newUUIDv7(t)
	base := time.Now().UTC().Truncate(time.Second)

	first, err := svc.push(ctx, userIDString(userA), pushRequest{
		DeviceID: deviceA.ID.String(),
		Items:    []pushItem{focusSessionItem(t, sessionID, base, base)},
	})
	if err != nil {
		t.Fatalf("push (userA): %v", err)
	}
	if first.Results[0].Status != "applied" {
		t.Fatalf("userA push status = %q, want applied", first.Results[0].Status)
	}

	second, err := svc.push(ctx, userIDString(userB), pushRequest{
		DeviceID: deviceB.ID.String(),
		Items:    []pushItem{focusSessionItem(t, sessionID, base.Add(time.Hour), base)},
	})
	if err != nil {
		t.Fatalf("push (userB, colliding id): %v", err)
	}
	if second.Results[0].Status != "invalid" || second.Results[0].Error == nil || second.Results[0].Error.Code != "FORBIDDEN" {
		t.Fatalf("userB push result = %+v, want invalid/FORBIDDEN", second.Results[0])
	}
}

// TestPushActivityEventUsesUserCategoryOverride exercises
// resolveCategory's user_app_settings override branch: once a user has an
// explicit category override for an app, subsequently pushed activity
// events for that app must be stored with the override's category_id
// rather than the app's (non-existent, here) default_category_id.
func TestPushActivityEventUsesUserCategoryOverride(t *testing.T) {
	pool := testPool(t)
	q := storedb.New(pool)
	user, device := newUser(t, q)
	svc := &Service{Queries: q}
	ctx := context.Background()
	suffix := randomSuffix(t)

	bundleID := "com.example.category-override." + suffix
	eventID := newUUIDv7(t)
	base := time.Now().UTC().Truncate(time.Second)

	// First push resolves (creates) the app row via resolveApp, with no
	// category override yet in place.
	_, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItem(t, eventID, bundleID, base, base.Add(time.Minute))},
	})
	if err != nil {
		t.Fatalf("push (create app, no override): %v", err)
	}

	var appID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM apps WHERE bundle_id = $1`, bundleID).Scan(&appID); err != nil {
		t.Fatalf("select apps: %v", err)
	}

	category, err := q.CreateCategoryForUser(ctx, storedb.CreateCategoryForUserParams{
		UserID: user.ID, Name: "Override Category " + suffix, Color: "#abcdef", Productivity: 1,
	})
	if err != nil {
		t.Fatalf("CreateCategoryForUser: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO user_app_settings (user_id, app_id, category_id, excluded, updated_at, server_seq)
		VALUES ($1, $2, $3, false, now(), nextval('server_seq_global'))`,
		user.ID, appID, category.ID,
	); err != nil {
		t.Fatalf("insert user_app_settings: %v", err)
	}

	secondEventID := newUUIDv7(t)
	secondResp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{activityEventItem(t, secondEventID, bundleID, base.Add(time.Hour), base.Add(time.Hour+time.Minute))},
	})
	if err != nil {
		t.Fatalf("push (with override): %v", err)
	}
	if secondResp.Results[0].Status != "applied" {
		t.Fatalf("push (with override) status = %q, want applied", secondResp.Results[0].Status)
	}

	var storedCategoryID pgtype.UUID
	if err := pool.QueryRow(ctx, `SELECT category_id FROM activity_events WHERE event_id = $1`, mustParseUUID(t, secondEventID)).Scan(&storedCategoryID); err != nil {
		t.Fatalf("select activity_events: %v", err)
	}
	if storedCategoryID != category.ID {
		t.Errorf("stored category_id = %+v, want the override's %+v", storedCategoryID, category.ID)
	}
}

// TestPullFocusSessionIncludesEndedAt pushes a completed focus session
// (ended_at set) and pulls it back, asserting ended_at is present on the
// wire, exercising formatOptionalTs's non-nil branch in pull.go — every
// other focus_session pushed by this package's tests is still "running"
// (no ended_at), so that branch is otherwise never reached.
func TestPullFocusSessionIncludesEndedAt(t *testing.T) {
	q := storedb.New(testPool(t))
	user, device := newUser(t, q)
	svc := &Service{Queries: q, Pool: nil}
	ctx := context.Background()

	sessionID := newUUIDv7(t)
	base := time.Now().UTC().Truncate(time.Second)
	endedAt := base.Add(30 * time.Minute).Format(timeLayout)

	data, err := json.Marshal(focusSessionData{
		ID:        sessionID,
		UpdatedAt: base.Format(timeLayout),
		StartedAt: base.Format(timeLayout),
		EndedAt:   &endedAt,
		Kind:      "focus",
		Status:    "completed",
	})
	if err != nil {
		t.Fatalf("marshal focusSessionData: %v", err)
	}

	pushResp, err := svc.push(ctx, userIDString(user), pushRequest{
		DeviceID: device.ID.String(),
		Items:    []pushItem{{EntityType: "focus_session", Data: data}},
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if pushResp.Results[0].Status != "applied" {
		t.Fatalf("push status = %q, want applied", pushResp.Results[0].Status)
	}

	pullResp, err := svc.pull(ctx, userIDString(user), "", 0)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	found := false
	for _, u := range pullResp.Changes["focus_sessions"].Upserts {
		dto, ok := u.(focusSessionUpsertDTO)
		if !ok {
			t.Fatalf("focus session upsert is not a focusSessionUpsertDTO: %T", u)
		}
		if dto.ID != sessionID {
			continue
		}
		found = true
		if dto.EndedAt == nil {
			t.Error("expected ended_at to be present")
		}
	}
	if !found {
		t.Fatalf("pulled focus sessions did not include %s", sessionID)
	}
}
