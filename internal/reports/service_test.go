package reports

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
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
// Migrations 000034-000036 recreated these caggs with
// timescaledb.materialized_only = false (RIZ-74), so a query against them
// already unions materialized data with a real-time read over the
// not-yet-materialized tail of activity_events — a manual refresh is no
// longer required for a closed period's totals to be correct. Tests still
// call this where they specifically want to assert against the
// materialized state (e.g. as a baseline before proving the real-time path
// picks up a later insert), rather than waiting on
// add_continuous_aggregate_policy's background schedule.
func refreshCaggs(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, view := range []string{"daily_category_totals", "daily_app_totals", "hourly_category_totals"} {
		if _, err := pool.Exec(ctx, "CALL refresh_continuous_aggregate($1, NULL, NULL)", view); err != nil {
			t.Fatalf("refresh_continuous_aggregate(%s): %v", view, err)
		}
	}
}

// futureDayBeyondWatermark returns a UTC day start guaranteed to be after
// TimescaleDB's current cross-cagg materialization watermark (see
// TestCategoryTotalsForRangeReflectsRealTimeDataWithNoRefresh's doc
// comment for why a fixed offset from "now" is not safe here): it reads
// _timescaledb_catalog.continuous_aggs_invalidation_threshold directly and
// picks whichever is later, "5 years from now" or "the day after the
// current watermark" — the watermark is -infinity (a very large negative
// microsecond count) on a freshly migrated database, in which case "5
// years from now" always wins.
func futureDayBeyondWatermark(t *testing.T, pool *pgxpool.Pool) time.Time {
	t.Helper()
	ctx := context.Background()

	future := time.Now().UTC().AddDate(5, 0, 0)
	candidate := time.Date(future.Year(), future.Month(), future.Day(), 0, 0, 0, 0, time.UTC)

	var watermarkMicros int64
	err := pool.QueryRow(ctx,
		"select coalesce(max(watermark), 0) from _timescaledb_catalog.continuous_aggs_invalidation_threshold",
	).Scan(&watermarkMicros)
	if err != nil {
		t.Fatalf("reading continuous_aggs_invalidation_threshold: %v", err)
	}
	if watermarkMicros == 0 {
		return candidate
	}
	watermark := time.UnixMicro(watermarkMicros).UTC()
	if watermark.Before(candidate) {
		return candidate
	}
	day := time.Date(watermark.Year(), watermark.Month(), watermark.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, 1)
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

// TestCategoriesCategoryIDFilterOnClosedDay exercises categoryTotals'
// cagg-path category_id post-filter branch (both the matching and
// non-matching/invalid cases).
func TestCategoriesCategoryIDFilterOnClosedDay(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Filtered Category")

	day := pastDay(11)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}
	matched, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{CategoryID: cat.ID.String()})
	if err != nil {
		t.Fatalf("Categories (category_id filter, match): %v", err)
	}
	var total int64
	for _, c := range matched.Categories {
		total += c.Seconds
	}
	if total != 10*60 {
		t.Errorf("total seconds = %d, want %d", total, 10*60)
	}

	unmatched, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{
		CategoryID: "00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("Categories (category_id filter, no match): %v", err)
	}
	if len(unmatched.Categories) != 0 {
		t.Errorf("Categories (category_id filter, no match) = %+v, want empty", unmatched.Categories)
	}

	if _, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{
		CategoryID: "not-a-valid-uuid",
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("Categories (invalid category_id) error = %v, want ErrValidation", err)
	}
}

// TestAppsDeviceFilterFallsBackToRaw exercises appTotals' raw-fallback
// branch (a device_id filter is incompatible with the daily_app_totals
// aggregate, per appTotals' doc comment), which TestAppsHappyPath's
// filter-less request never reaches.
func TestAppsDeviceFilterFallsBackToRaw(t *testing.T) {
	q := testQueries(t)
	user, deviceA, deviceB := newUserAndDevices(t, q)

	day := pastDay(8)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+10*time.Minute), eventOpts{})
	insertEvent(t, q, user.ID, deviceB.ID, day.Add(2*time.Hour), day.Add(2*time.Hour+20*time.Minute), eventOpts{})

	svc := &Service{Queries: q}
	result, err := svc.Apps(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{DeviceID: deviceA.ID.String()})
	if err != nil {
		t.Fatalf("Apps: %v", err)
	}
	var total int64
	for _, a := range result.Apps {
		total += a.Seconds
	}
	if total != 10*60 {
		t.Errorf("total seconds = %d, want %d (only deviceA's event)", total, 10*60)
	}
}

// TestAppsAppIDFilterOnClosedDay exercises appTotals' cagg-path app_id
// post-filter branch.
func TestAppsAppIDFilterOnClosedDay(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)

	day := pastDay(9)
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(time.Hour), day.Add(time.Hour+15*time.Minute), eventOpts{})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}
	unfiltered, err := svc.Apps(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Apps (unfiltered): %v", err)
	}
	if len(unfiltered.Apps) == 0 {
		t.Fatal("expected at least one app bucket to filter by")
	}

	// Filtering by an app_id that doesn't match anything must return an
	// empty (not error) result, per appTotals' filtered-but-absent branch.
	filtered, err := svc.Apps(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{
		AppID: "00000000-0000-0000-0000-000000000000",
	})
	if err != nil {
		t.Fatalf("Apps (app_id filter, no match): %v", err)
	}
	if len(filtered.Apps) != 0 {
		t.Errorf("Apps (app_id filter, no match) = %+v, want empty", filtered.Apps)
	}

	if _, err := svc.Apps(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{
		AppID: "not-a-valid-uuid",
	}); !errors.Is(err, ErrValidation) {
		t.Errorf("Apps (invalid app_id) error = %v, want ErrValidation", err)
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

// TestCategoriesClosedDaySameDeviceFullyOverlappingCapMatchesRawMerge is a
// regression test (RIZ-74, PR #9 review follow-up): before migration
// 000035 added a device_id dimension to daily_category_totals, the
// closed-period cagg path summed duration_s with no bound on same-device
// overlap at all, so it could inflate a total by an unbounded amount
// relative to the raw/open-period path's interval merge
// (documentation/sync-protocol.md §Overlap Rules; internal/reports/trim.go).
// Two identical full-day events on the same device push the naive sum to
// double the day's length, entirely inside one closed day.
//
// This specific input — full-day-spanning, fully-overlapping intervals —
// is the one case where the cagg path's per-device window cap
// (internal/store/queries/activities.sql's CategoryTotalsForRange:
// LEAST(device_total_s, window_seconds)) and the raw path's exact interval
// merge (internal/reports/trim.go) necessarily agree: the merged coverage
// spans the whole window, so both the cap and the merge land on exactly
// the window's length. This is NOT a general equivalence claim — see
// TestCategoriesClosedDayPartialOverlapCagCapDivergesFromRawMerge below
// for an input where the two paths disagree — only that this cap removes
// the previously-unbounded inflation for the case where it happens to be
// exact.
func TestCategoriesClosedDaySameDeviceFullyOverlappingCapMatchesRawMerge(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	day := pastDay(12)
	// Two identical full-day-spanning events on the SAME device: the
	// naive sum (2 * 24h = 48h) would double-count the day, but the
	// merged/capped coverage is exactly the day's own 24h.
	insertEvent(t, q, user.ID, deviceA.ID, day, day.AddDate(0, 0, 1), eventOpts{CategoryID: cat.ID})
	insertEvent(t, q, user.ID, deviceA.ID, day, day.AddDate(0, 0, 1), eventOpts{CategoryID: cat.ID})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}

	caggResult, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Categories (cagg path): %v", err)
	}
	var caggTotal int64
	for _, c := range caggResult.Categories {
		caggTotal += c.Seconds
	}

	// A precision filter is incompatible with the cagg fast path, forcing
	// categoryTotals to fall back to rawTotals for the whole range.
	rawResult, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{Precision: "exact"})
	if err != nil {
		t.Fatalf("Categories (raw path): %v", err)
	}
	var rawTotal int64
	for _, c := range rawResult.Categories {
		rawTotal += c.Seconds
	}

	const wantMerged = 24 * 60 * 60
	if caggTotal != wantMerged {
		t.Errorf("cagg-path total seconds = %d, want %d (same-device overlap must be capped, not summed)", caggTotal, wantMerged)
	}
	if rawTotal != wantMerged {
		t.Errorf("raw-path total seconds = %d, want %d (same-device overlap must be merged, not summed)", rawTotal, wantMerged)
	}
	if caggTotal != rawTotal {
		t.Errorf("cagg-path total (%d) and raw-path total (%d) disagree; expected them to agree for this fully-overlapping-full-window input", caggTotal, rawTotal)
	}
}

// TestCategoriesClosedDayPartialOverlapCagCapDivergesFromRawMerge documents
// (RIZ-74, PR #9 review follow-up) the known, deliberate divergence between
// the closed-period cagg path's per-device window cap and the raw path's
// exact interval merge: the cap
// (internal/store/queries/activities.sql's CategoryTotalsForRange:
// LEAST(device_total_s, window_seconds)) only removes overlap severe
// enough to push a device's naive summed total_s above the window's own
// length. A same-device overlap that doesn't reach that threshold still
// passes through uncapped, so the cagg path can overstate a total
// relative to the raw path's true merged duration.
//
// Two same-device events over a 24h closed day, 00:00-06:00 and
// 03:00-09:00 (naive sum 12h, merged/raw coverage 9h): the raw path
// returns 9h; the cagg path returns min(12h, 24h) = 12h, since 12h never
// exceeds the 24h window. This is the exact counterexample from
// CategoryTotalsForRange's doc comment, asserted here as a regression
// test so a future change can't silently start claiming (or requiring)
// exact equivalence between the two paths.
func TestCategoriesClosedDayPartialOverlapCagCapDivergesFromRawMerge(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	day := pastDay(13)
	// Same-device, partially-overlapping events: naive sum 12h, merged
	// (raw) coverage 9h (00:00-09:00).
	insertEvent(t, q, user.ID, deviceA.ID, day, day.Add(6*time.Hour), eventOpts{CategoryID: cat.ID})
	insertEvent(t, q, user.ID, deviceA.ID, day.Add(3*time.Hour), day.Add(9*time.Hour), eventOpts{CategoryID: cat.ID})
	refreshCaggs(t, pool)

	svc := &Service{Queries: q}

	caggResult, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{})
	if err != nil {
		t.Fatalf("Categories (cagg path): %v", err)
	}
	var caggTotal int64
	for _, c := range caggResult.Categories {
		caggTotal += c.Seconds
	}

	// A precision filter is incompatible with the cagg fast path, forcing
	// categoryTotals to fall back to rawTotals for the whole range.
	rawResult, err := svc.Categories(context.Background(), user.ID.String(), day, day.AddDate(0, 0, 1), reportFilters{Precision: "exact"})
	if err != nil {
		t.Fatalf("Categories (raw path): %v", err)
	}
	var rawTotal int64
	for _, c := range rawResult.Categories {
		rawTotal += c.Seconds
	}

	const wantRaw = 9 * 60 * 60
	const wantCagg = 12 * 60 * 60
	if rawTotal != wantRaw {
		t.Errorf("raw-path total seconds = %d, want %d (exact merged coverage)", rawTotal, wantRaw)
	}
	if caggTotal != wantCagg {
		t.Errorf("cagg-path total seconds = %d, want %d (window cap does not remove overlap below the window's own length)", caggTotal, wantCagg)
	}
	if caggTotal == rawTotal {
		t.Errorf("cagg-path total (%d) unexpectedly matches raw-path total (%d); this input is meant to demonstrate the two paths diverge", caggTotal, rawTotal)
	}
}

// TestReportCaggsAreConfiguredMaterializedOnlyFalse is a schema-level check
// (RIZ-74, PR #9 review follow-up) that migrations 000034-000036 actually
// left daily_app_totals, daily_category_totals, and hourly_category_totals
// configured as real-time (materialized_only = false) continuous
// aggregates, per documentation/architecture-backend.md §Aggregation
// Strategy's anticipated fix.
func TestReportCaggsAreConfiguredMaterializedOnlyFalse(t *testing.T) {
	pool := testDBPool(t)
	ctx := context.Background()

	for _, view := range []string{"daily_app_totals", "daily_category_totals", "hourly_category_totals"} {
		var materializedOnly bool
		err := pool.QueryRow(ctx,
			"select materialized_only from timescaledb_information.continuous_aggregates where view_name = $1", view,
		).Scan(&materializedOnly)
		if err != nil {
			t.Fatalf("querying materialized_only for %s: %v", view, err)
		}
		if materializedOnly {
			t.Errorf("%s: materialized_only = true, want false (migrations 000034-000036)", view)
		}
	}
}

// TestCategoryTotalsForRangeReflectsRealTimeDataWithNoRefresh is a
// real-time-aggregation proof (RIZ-74, PR #9 review follow-up): migration
// 000035 recreated daily_category_totals with timescaledb.materialized_only
// = false, so a query against it unions materialized data with a live read
// over not-yet-materialized activity_events rows, rather than a plain
// materialized_only = true view (the pre-RIZ-74 shape), which would return
// nothing for a bucket it has never refreshed.
//
// This deliberately uses a window in the future rather than "yesterday" or
// "today". TimescaleDB's materialization watermark is a single value
// shared by every continuous aggregate on the activity_events hypertable,
// and a full (NULL, NULL) refresh_continuous_aggregate call — several
// other tests in this package deliberately make one, to establish a
// materialized baseline for their own assertions — advances it to (one
// bucket past) the highest started_at value that exists in the hypertable
// at the time of that call, and it never moves backward. That includes
// "today": the first such call any earlier test makes, in the same
// process, against data that already spans up to "now", pushes the
// watermark to tomorrow's bucket boundary, which would freeze today's
// bucket for the rest of the process — so testing this via "today" or a
// recent past day is order-dependent on which other tests in this process
// happen to refresh first. Every other test in this package only ever
// inserts past-or-present timestamps, so a window far enough in the
// future can never be behind a watermark any of them could have produced.
//
// futureDayBeyondWatermark below also reads the watermark directly and
// picks a day strictly after it, rather than a fixed offset from "now":
// a fixed offset would eventually self-poison across repeated `go test`
// invocations against the same never-recreated local dev database — an
// unrelated test's later refresh would pick up this test's own previous
// leftover future row as its new "highest timestamp" and advance the
// watermark to match it, so a second local run choosing the exact same
// day would then find itself behind the (now-advanced) watermark too.
// Reading the watermark first makes this test correct both on a freshly
// migrated database (CI, and this ticket's Definition of Done) and across
// any number of repeated local runs against the same database.
//
// This calls CategoryTotalsForRange directly (bypassing Service.Categories)
// specifically because splitClosedOpen always treats a from/to range at or
// after "today" as open, routing it to the raw pass — so exercising this
// query with a future window requires bypassing that routing, not because
// the cagg-path routing itself is in question.
func TestCategoryTotalsForRangeReflectsRealTimeDataWithNoRefresh(t *testing.T) {
	pool := testDBPool(t)
	q := storedb.New(pool)
	user, deviceA, _ := newUserAndDevices(t, q)
	cat := newCategory(t, q, user.ID, "Development")

	from := futureDayBeyondWatermark(t, pool)
	to := from.AddDate(0, 0, 1)
	insertEvent(t, q, user.ID, deviceA.ID, from.Add(9*time.Hour), from.Add(9*time.Hour+20*time.Minute), eventOpts{CategoryID: cat.ID})

	// No refresh_continuous_aggregate call anywhere in this test: nothing
	// this far in the future could ever have been materialized by any
	// refresh, by any test, so a non-empty result here can only come from
	// the real-time read path.
	rows, err := q.CategoryTotalsForRange(context.Background(), storedb.CategoryTotalsForRangeParams{
		UserID:        user.ID,
		FromDay:       pgTimestamptz(from),
		ToDay:         pgTimestamptz(to),
		WindowSeconds: int64(to.Sub(from).Seconds()),
	})
	if err != nil {
		t.Fatalf("CategoryTotalsForRange: %v", err)
	}

	var total int64
	for _, r := range rows {
		total += r.TotalS
	}
	const want = 20 * 60
	if total != want {
		t.Errorf("total seconds = %d, want %d (real-time aggregation must include a row that has never been materialized)", total, want)
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
