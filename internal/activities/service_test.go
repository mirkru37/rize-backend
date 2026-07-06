package activities_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/activities"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func testQueries(t *testing.T) *storedb.Queries {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping activities integration test")
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
	return storedb.New(pool)
}

func textPtr(s string) *string { return &s }

func newUserAndDevices(t *testing.T, q *storedb.Queries) (storedb.User, storedb.Device, storedb.Device) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email: textPtr(fmt.Sprintf("activities-test+%s@example.com", suffix)), DisplayName: textPtr("Test User"), Role: "user", Timezone: textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	deviceA, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{UserID: user.ID, Platform: "macos", Name: "device-a", Model: "m", OsVersion: "1", AppVersion: "1"})
	if err != nil {
		t.Fatalf("CreateDevice a: %v", err)
	}
	deviceB, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{UserID: user.ID, Platform: "ios", Name: "device-b", Model: "m", OsVersion: "1", AppVersion: "1"})
	if err != nil {
		t.Fatalf("CreateDevice b: %v", err)
	}
	return user, deviceA, deviceB
}

func insertEvent(t *testing.T, q *storedb.Queries, userID, deviceID pgtype.UUID, started, ended time.Time, precision string) storedb.ActivityEvent {
	t.Helper()
	ctx := context.Background()
	eventID, err := newUUIDv4Test()
	if err != nil {
		t.Fatalf("newUUIDv4Test: %v", err)
	}
	row, err := q.InsertActivityEvent(ctx, storedb.InsertActivityEventParams{
		EventID:   eventID,
		UserID:    userID,
		DeviceID:  deviceID,
		StartedAt: pgtype.Timestamptz{Time: started, Valid: true},
		EndedAt:   pgtype.Timestamptz{Time: ended, Valid: true},
		Type:      "app_active",
		Source:    "desktop",
		Precision: precision,
	})
	if err != nil {
		t.Fatalf("InsertActivityEvent: %v", err)
	}
	return row
}

func newUUIDv4Test() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}

func TestListHappyPathAndTimeRangeFilter(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)

	base := time.Date(2020, 1, 1, 9, 0, 0, 0, time.UTC)
	insertEvent(t, q, user.ID, deviceA.ID, base, base.Add(10*time.Minute), "exact")
	insertEvent(t, q, user.ID, deviceA.ID, base.Add(2*time.Hour), base.Add(2*time.Hour+10*time.Minute), "exact")

	svc := &activities.Service{Queries: q}
	items, _, hasMore, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{
		From: base.Add(-time.Hour),
		To:   base.Add(time.Hour), // should only include the first event
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	if hasMore {
		t.Errorf("hasMore = true, want false")
	}
}

func TestListPrecisionFilter(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)

	base := time.Date(2020, 2, 1, 9, 0, 0, 0, time.UTC)
	insertEvent(t, q, user.ID, deviceA.ID, base, base.Add(10*time.Minute), "exact")
	insertEvent(t, q, user.ID, deviceA.ID, base.Add(time.Hour), base.Add(time.Hour+10*time.Minute), "approximate")

	svc := &activities.Service{Queries: q}
	items, _, _, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{
		From:      base.Add(-time.Hour),
		To:        base.Add(2 * time.Hour),
		Precision: "approximate",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Precision != "approximate" {
		t.Fatalf("items = %+v, want exactly one approximate event", items)
	}
}

func TestListTenantIsolation(t *testing.T) {
	q := testQueries(t)
	userA, deviceA, _ := newUserAndDevices(t, q)
	userB, _, _ := newUserAndDevices(t, q)

	base := time.Date(2020, 3, 1, 9, 0, 0, 0, time.UTC)
	insertEvent(t, q, userA.ID, deviceA.ID, base, base.Add(10*time.Minute), "exact")

	svc := &activities.Service{Queries: q}
	items, _, _, err := svc.List(context.Background(), userB.ID.String(), activities.ListParams{
		From: base.Add(-time.Hour),
		To:   base.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("len(items) = %d, want 0 (userB must not see userA's events)", len(items))
	}
}

func TestListInvalidRangeRejected(t *testing.T) {
	q := testQueries(t)
	user, _, _ := newUserAndDevices(t, q)

	svc := &activities.Service{Queries: q}
	from := time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC)
	to := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC) // from > to
	_, _, _, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{From: from, To: to})
	if err == nil {
		t.Fatalf("expected an error for from > to")
	}
}

func TestListAbsurdRangeRejected(t *testing.T) {
	q := testQueries(t)
	user, _, _ := newUserAndDevices(t, q)

	svc := &activities.Service{Queries: q}
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // way over MaxReportRange
	_, _, _, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{From: from, To: to})
	if err == nil {
		t.Fatalf("expected an error for an absurdly wide range")
	}
}

func TestListPagination(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)

	base := time.Date(2020, 4, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		start := base.Add(time.Duration(i) * time.Hour)
		insertEvent(t, q, user.ID, deviceA.ID, start, start.Add(time.Minute), "exact")
	}

	svc := &activities.Service{Queries: q}
	page1, cursor1, hasMore1, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{
		From: base.Add(-time.Hour), To: base.Add(24 * time.Hour), Limit: 2,
	})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	if len(page1) != 2 || !hasMore1 {
		t.Fatalf("page1 = %d items, hasMore=%v; want 2 items, hasMore=true", len(page1), hasMore1)
	}

	page2, _, hasMore2, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{
		From: base.Add(-time.Hour), To: base.Add(24 * time.Hour), Limit: 2, Cursor: cursor1,
	})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(page2) != 1 || hasMore2 {
		t.Fatalf("page2 = %d items, hasMore=%v; want 1 item, hasMore=false", len(page2), hasMore2)
	}
}

func TestListFilterCombinationAppAndDevice(t *testing.T) {
	q := testQueries(t)
	user, deviceA, deviceB := newUserAndDevices(t, q)

	base := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	insertEvent(t, q, user.ID, deviceA.ID, base, base.Add(time.Minute), "exact")
	insertEvent(t, q, user.ID, deviceB.ID, base.Add(time.Hour), base.Add(time.Hour+time.Minute), "exact")

	svc := &activities.Service{Queries: q}
	items, _, _, err := svc.List(context.Background(), user.ID.String(), activities.ListParams{
		From: base.Add(-time.Hour), To: base.Add(2 * time.Hour), DeviceID: deviceA.ID.String(),
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].DeviceID != deviceA.ID {
		t.Fatalf("items = %+v, want exactly deviceA's event", items)
	}
}
