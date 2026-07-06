package reports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

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
	return storedb.New(testDBPool(t))
}

func testDBPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping reports integration test")
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

// refreshCaggs forces an immediate materialization of the three
// continuous aggregates this package reads from, per
// documentation/database-schema.md's Continuous Aggregates section.
// migrations 000016-000018 create these caggs with materialized_only=true
// (Timescale's current default for newly-created continuous aggregates),
// so newly-inserted rows for a "closed" period are only reflected once a
// refresh runs — normally driven by migration 000019's
// add_continuous_aggregate_policy schedules. Tests call this against the
// same pool used to seed data, to deterministically simulate "the
// aggregate has since caught up" rather than waiting on that background
// schedule.
func refreshCaggs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, view := range []string{"daily_category_totals", "daily_app_totals", "hourly_category_totals"} {
		if _, err := pool.Exec(ctx, "CALL refresh_continuous_aggregate($1, NULL, NULL)", view); err != nil {
			t.Fatalf("refresh_continuous_aggregate(%s): %v", view, err)
		}
	}
}

func textPtr(s string) *string { return &s }

func newUserAndDevices(t *testing.T, q *storedb.Queries) (storedb.User, storedb.Device, storedb.Device) {
	t.Helper()
	ctx := context.Background()
	suffix := randomSuffix(t)

	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email: textPtr(fmt.Sprintf("reports-test+%s@example.com", suffix)), DisplayName: textPtr("Test User"), Role: "user", Timezone: textPtr("UTC"),
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

func newCategory(t *testing.T, q *storedb.Queries, userID pgtype.UUID, name string) storedb.Category {
	t.Helper()
	cat, err := q.CreateCategoryForUser(context.Background(), storedb.CreateCategoryForUserParams{
		UserID: userID, Name: name, Color: "#ffffff", Productivity: 1,
	})
	if err != nil {
		t.Fatalf("CreateCategoryForUser: %v", err)
	}
	return cat
}

func newUUIDv4Test(t *testing.T) pgtype.UUID {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}
}

type eventOpts struct {
	CategoryID pgtype.UUID
	Precision  string
}

func insertEvent(t *testing.T, q *storedb.Queries, userID, deviceID pgtype.UUID, started, ended time.Time, opts eventOpts) storedb.ActivityEvent {
	t.Helper()
	precision := opts.Precision
	if precision == "" {
		precision = "exact"
	}
	row, err := q.InsertActivityEvent(context.Background(), storedb.InsertActivityEventParams{
		EventID:    newUUIDv4Test(t),
		UserID:     userID,
		DeviceID:   deviceID,
		StartedAt:  pgtype.Timestamptz{Time: started, Valid: true},
		EndedAt:    pgtype.Timestamptz{Time: ended, Valid: true},
		Type:       "app_active",
		Source:     "desktop",
		Precision:  precision,
		CategoryID: opts.CategoryID,
	})
	if err != nil {
		t.Fatalf("InsertActivityEvent: %v", err)
	}
	return row
}

// pastDay returns a UTC day far enough in the past that it is always a
// "closed" period relative to time.Now() when tests run, so cagg-path
// assertions are stable regardless of wall-clock time-of-day.
func pastDay(daysAgo int) time.Time {
	now := time.Now().UTC()
	base := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	return base.AddDate(0, 0, -daysAgo)
}

func TestDailyHappyPathClosedDay(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	day := pastDay(10)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(9*time.Hour), day.Add(9*time.Hour+30*time.Minute), eventOpts{CategoryID: cat.ID})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}
	result, err := svc.Daily(context.Background(), user.ID.String(), day, reportFilters{})
	if err != nil {
		t.Fatalf("Daily: %v", err)
	}
	if result.TotalTrackedSeconds != 30*60 {
		t.Errorf("TotalTrackedSeconds = %d, want %d", result.TotalTrackedSeconds, 30*60)
	}
	found := false
	for _, c := range result.Categories {
		if c.Name == "Development" && c.Seconds == 30*60 {
			found = true
		}
	}
	if !found {
		t.Errorf("Categories = %+v, want a Development bucket with 1800s", result.Categories)
	}
}

func TestSummaryTenantIsolation(t *testing.T) {
	q := testQueries(t)
	userA, deviceA, _ := newUserAndDevices(t, q)
	userB, _, _ := newUserAndDevices(t, q)

	day := pastDay(5)
	insertEvent(t, q, userA.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{})

	svc := &Service{Queries: q}
	result, err := svc.Summary(context.Background(), userB.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if result.TotalTrackedSeconds != 0 {
		t.Errorf("TotalTrackedSeconds = %d, want 0 (userB must not see userA's events)", result.TotalTrackedSeconds)
	}
}

func TestSummaryInvalidRangeRejected(t *testing.T) {
	q := testQueries(t)
	user, _, _ := newUserAndDevices(t, q)

	svc := &Service{Queries: q}
	from := time.Now().UTC()
	to := from.Add(-time.Hour) // from > to
	_, err := svc.Summary(context.Background(), user.ID.String(), from, to, reportFilters{})
	if err == nil {
		t.Fatalf("expected an error for from > to")
	}
}

func TestSummaryAbsurdRangeRejected(t *testing.T) {
	q := testQueries(t)
	user, _, _ := newUserAndDevices(t, q)

	svc := &Service{Queries: q}
	from := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := svc.Summary(context.Background(), user.ID.String(), from, to, reportFilters{})
	if err == nil {
		t.Fatalf("expected an error for an absurdly wide range")
	}
}

func TestCategoriesPrecisionFilter(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Communication")

	day := pastDay(3)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID, Precision: "exact"})
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(2*time.Hour), day.Add(2*time.Hour+20*time.Minute), eventOpts{CategoryID: cat.ID, Precision: "approximate"})

	svc := &Service{Queries: q}
	result, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{Precision: "approximate"})
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	var total int64
	for _, c := range result.Categories {
		total += c.Seconds
	}
	if total != 20*60 {
		t.Errorf("total seconds = %d, want %d (only the approximate event)", total, 20*60)
	}
}

func TestCategoriesCurrentPeriodRawTrimSameDeviceCapped(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	now := time.Now().UTC()
	// Two overlapping events TODAY from the SAME device: the open/current
	// period must go through the raw-event trim pass, per
	// documentation/architecture-backend.md §Aggregation Strategy.
	start := now.Add(-2 * time.Hour)
	insertEvent(t, q, user.ID, deviceA.ID, start, start.Add(30*time.Minute), eventOpts{CategoryID: cat.ID})
	insertEvent(t, q, user.ID, deviceA.ID, start.Add(15*time.Minute), start.Add(45*time.Minute), eventOpts{CategoryID: cat.ID})

	svc := &Service{Queries: q}
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	result, err := svc.Categories(context.Background(), user.ID.String(), from, to, reportFilters{})
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	var total int64
	for _, c := range result.Categories {
		total += c.Seconds
	}
	// Merged coverage is 45m (start..start+45m), not the naive 60m sum of
	// two 30m intervals.
	if total != 45*60 {
		t.Errorf("total seconds = %d, want %d (same-device overlap must be capped)", total, 45*60)
	}
}

func TestCategoriesCurrentPeriodCrossDeviceNotCapped(t *testing.T) {
	q := testQueries(t)
	user, deviceA, deviceB := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	now := time.Now().UTC()
	start := now.Add(-2 * time.Hour)
	insertEvent(t, q, user.ID, deviceA.ID, start, start.Add(30*time.Minute), eventOpts{CategoryID: cat.ID})
	insertEvent(t, q, user.ID, deviceB.ID, start.Add(15*time.Minute), start.Add(45*time.Minute), eventOpts{CategoryID: cat.ID})

	svc := &Service{Queries: q}
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	result, err := svc.Categories(context.Background(), user.ID.String(), from, to, reportFilters{})
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	var total int64
	for _, c := range result.Categories {
		total += c.Seconds
	}
	// Two different devices: both 30m intervals count in full = 60m.
	if total != 60*60 {
		t.Errorf("total seconds = %d, want %d (cross-device overlap must not be trimmed)", total, 60*60)
	}
}

func TestAppsHappyPath(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)

	day := pastDay(7)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+15*time.Minute), eventOpts{})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}
	result, err := svc.Apps(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	var total int64
	for _, a := range result.Apps {
		total += a.Seconds
	}
	if total != 15*60 {
		t.Errorf("total seconds = %d, want %d", total, 15*60)
	}
}

func TestProjectsHappyPathAlwaysRaw(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)

	day := pastDay(20)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{})

	svc := &Service{Queries: q}
	result, err := svc.Projects(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	var total int64
	for _, p := range result.Projects {
		total += p.Seconds
	}
	if total != 10*60 {
		t.Errorf("total seconds = %d, want %d", total, 10*60)
	}
}

func TestTimelineHappyPathAndPagination(t *testing.T) {
	q := testQueries(t)
	user, deviceA, _ := newUserAndDevices(t, q)

	day := pastDay(1)
	for i := 0; i < 3; i++ {
		start := day.Add(time.Duration(i) * time.Hour)
		insertEvent(t, q, user.ID, deviceA.ID, start, start.Add(time.Minute), eventOpts{})
	}

	svc := &Service{Queries: q}
	items, cursor, hasMore, err := svc.Timeline(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{}, "", 2)
	if err != nil {
		t.Fatalf("Timeline: %v", err)
	}
	if len(items) != 2 || !hasMore {
		t.Fatalf("page1 = %d items, hasMore=%v; want 2, true", len(items), hasMore)
	}

	items2, _, hasMore2, err := svc.Timeline(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{}, cursor, 2)
	if err != nil {
		t.Fatalf("Timeline page2: %v", err)
	}
	if len(items2) != 1 || hasMore2 {
		t.Fatalf("page2 = %d items, hasMore=%v; want 1, false", len(items2), hasMore2)
	}
}

func TestCategoriesInvalidPrecisionRejected(t *testing.T) {
	q := testQueries(t)
	user, _, _ := newUserAndDevices(t, q)

	svc := &Service{Queries: q}
	from := pastDay(2)
	_, err := svc.Categories(context.Background(), user.ID.String(), from, from.AddDate(0, 0, 1), reportFilters{Precision: "bogus"})
	if err == nil {
		t.Fatalf("expected an error for an invalid precision filter")
	}
}

func TestCategoriesDeviceFilterFallsBackToRawForClosedDay(t *testing.T) {
	q := testQueries(t)
	user, deviceA, deviceB := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	day := pastDay(15)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID})
	insertEvent(t, q, user.ID, deviceB.ID, day.Add(2*time.Hour), day.Add(2*time.Hour+20*time.Minute), eventOpts{CategoryID: cat.ID})

	svc := &Service{Queries: q}
	result, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{DeviceID: deviceA.ID.String()})
	if err != nil {
		t.Fatalf("Categories: %v", err)
	}
	var total int64
	for _, c := range result.Categories {
		total += c.Seconds
	}
	if total != 10*60 {
		t.Errorf("total seconds = %d, want %d (device filter must scope even a closed-day report)", total, 10*60)
	}
}
