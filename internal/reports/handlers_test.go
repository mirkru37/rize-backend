package reports

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// newReportsTestRouter wires this package's six GET /v1/reports/* routes
// behind chi with the real Authenticate middleware, and returns an access
// token for a freshly created user with a device (reusing
// newUserAndDevices from service_test.go).
func newReportsTestRouter(t *testing.T) (*chi.Mux, string) {
	t.Helper()
	r, _, token, _, _ := newReportsTestRouterWithQueries(t)
	return r, token
}

// newReportsTestRouterWithQueries is newReportsTestRouter plus the
// underlying *storedb.Queries and created user/device, for tests that need
// to seed data (e.g. an activity_events row) before calling an endpoint.
func newReportsTestRouterWithQueries(t *testing.T) (*chi.Mux, *storedb.Queries, string, storedb.User, storedb.Device) {
	t.Helper()
	q := testQueries(t)
	svc := &Service{Queries: q}
	h := NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	user, deviceA, _ := newUserAndDevices(t, q)
	token, err := auth.IssueAccessToken(key, user.ID.String(), "user", "", time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return r, q, token, user, deviceA
}

func doReportsReq(t *testing.T, r http.Handler, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// TestHTTP_ReportsSummary seeds a categorized event so the response's
// "categories" breakdown is non-empty, exercising toCategoryBreakdown.
func TestHTTP_ReportsSummary(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)
	cat := newCategory(t, q, user.ID, "Deep Work")

	// Use "today" (the open/current period) rather than a closed past day,
	// so the result comes from the raw-event pass without needing to wait
	// on (or manually trigger) a continuous aggregate refresh.
	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	insertEvent(t, q, user.ID, device.ID, now.Add(-time.Hour), now.Add(-time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID})

	rec := doReportsReq(t, r, "/v1/reports/summary?from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Categories []map[string]any `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Categories) == 0 {
		t.Fatal("expected a non-empty categories breakdown")
	}
}

func TestHTTP_ReportsSummaryUnauthenticated(t *testing.T) {
	r, _ := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/summary?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ReportsSummaryMissingFromTo(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/summary", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsSummaryInternalError exercises writeServiceError's
// default (500) branch via an already-canceled request context.
func TestHTTP_ReportsSummaryInternalError(t *testing.T) {
	r, token := newReportsTestRouter(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/reports/summary?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsDaily seeds a categorized event on the queried day
// (today, the open period) so the response's "categories" breakdown is
// non-empty.
func TestHTTP_ReportsDaily(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)
	cat := newCategory(t, q, user.ID, "Deep Work")

	now := time.Now().UTC()
	insertEvent(t, q, user.ID, device.ID, now.Add(-time.Hour), now.Add(-time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID})

	rec := doReportsReq(t, r, "/v1/reports/daily?date="+now.Format("2006-01-02"), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Categories []map[string]any `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Categories) == 0 {
		t.Fatal("expected a non-empty categories breakdown")
	}
}

func TestHTTP_ReportsDailyInvalidDate(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/daily?date=not-a-date", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsDailyInvalidPrecision exercises Daily's own
// f.validatePrecision() call, distinct from Categories'/Apps' equivalent
// checks (which run inside categoryTotals/appTotals's raw-fallback path
// instead).
func TestHTTP_ReportsDailyInvalidPrecision(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/daily?date=2024-01-01&precision=bogus", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsProjectsInvalidPrecisionFilter exercises rawTotals'
// precision-validation branch via the always-raw Projects endpoint (the
// Apps equivalent test covers appTotals' raw-fallback dispatch instead).
func TestHTTP_ReportsProjectsInvalidPrecisionFilter(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/projects?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z&precision=bogus", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsTimelineInvalidFilters is table-driven coverage for
// Timeline's filter-validation branches.
func TestHTTP_ReportsTimelineInvalidFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "invalid app_id", query: "&app_id=not-a-uuid"},
		{name: "invalid category_id", query: "&category_id=not-a-uuid"},
		{name: "invalid project_id", query: "&project_id=not-a-uuid"},
		{name: "invalid device_id", query: "&device_id=not-a-uuid"},
		{name: "invalid precision", query: "&precision=bogus"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, token := newReportsTestRouter(t)

			rec := doReportsReq(t, r, "/v1/reports/timeline?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z"+tt.query, token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHTTP_ReportsCategories seeds a categorized event over the open
// period so the response's "categories" breakdown is non-empty.
func TestHTTP_ReportsCategories(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)
	cat := newCategory(t, q, user.ID, "Communication")

	now := time.Now().UTC()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	to := from.AddDate(0, 0, 1)
	insertEvent(t, q, user.ID, device.ID, now.Add(-time.Hour), now.Add(-time.Hour+10*time.Minute), eventOpts{CategoryID: cat.ID})

	rec := doReportsReq(t, r, "/v1/reports/categories?from="+from.Format(time.RFC3339)+"&to="+to.Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Categories []map[string]any `json:"categories"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Categories) == 0 {
		t.Fatal("expected a non-empty categories breakdown")
	}
}

func TestHTTP_ReportsCategoriesInvalidRange(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/categories?from=2024-01-02T00:00:00Z&to=2024-01-01T00:00:00Z", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsApps seeds an event over the open period so the
// response's "apps" breakdown is non-empty, exercising toAppBreakdown.
func TestHTTP_ReportsApps(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)

	now := time.Now().UTC()
	insertEvent(t, q, user.ID, device.ID, now.Add(-time.Hour), now.Add(-time.Hour+15*time.Minute), eventOpts{})

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rec := doReportsReq(t, r, "/v1/reports/apps?from="+from.Format(time.RFC3339)+"&to="+from.AddDate(0, 0, 1).Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Apps []map[string]any `json:"apps"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Apps) == 0 {
		t.Fatal("expected a non-empty apps breakdown")
	}
}

// TestHTTP_ReportsProjects seeds an event over the open period so the
// response's "projects" breakdown is non-empty, exercising
// toProjectBreakdown.
func TestHTTP_ReportsProjects(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)

	now := time.Now().UTC()
	insertEvent(t, q, user.ID, device.ID, now.Add(-time.Hour), now.Add(-time.Hour+10*time.Minute), eventOpts{})

	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	rec := doReportsReq(t, r, "/v1/reports/projects?from="+from.Format(time.RFC3339)+"&to="+from.AddDate(0, 0, 1).Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Projects []map[string]any `json:"projects"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Projects) == 0 {
		t.Fatal("expected a non-empty projects breakdown")
	}
}

// TestHTTP_ReportsProjectsInvalidDeviceIDFilter exercises rawTotals'
// device_id-filter validation branch via the always-raw Projects endpoint.
func TestHTTP_ReportsProjectsInvalidDeviceIDFilter(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/projects?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z&device_id=not-a-uuid", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsAppsInvalidPrecisionFilter exercises rawTotals'
// precision-filter validation branch.
func TestHTTP_ReportsAppsInvalidPrecisionFilter(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/apps?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z&precision=bogus", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ReportsTimeline seeds one activity_events row so the response's
// "data" array is non-empty, exercising toTimelineEventDTO (the handler's
// row-to-wire-DTO conversion), which an empty result never reaches.
func TestHTTP_ReportsTimeline(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)

	day := pastDay(1)
	insertEvent(t, q, user.ID, device.ID, day.Add(time.Hour), day.Add(time.Hour+time.Minute), eventOpts{})

	rec := doReportsReq(t, r, "/v1/reports/timeline?from="+day.Format(time.RFC3339)+"&to="+day.AddDate(0, 0, 1).Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(body.Data))
	}
}

// TestHTTP_ReportsTimelineEnrichedEvent seeds an event with app_id,
// category_id, and project_id all set, exercising toTimelineEventDTO's
// enrichment branches, which TestHTTP_ReportsTimeline's bare event leaves
// unexercised.
func TestHTTP_ReportsTimelineEnrichedEvent(t *testing.T) {
	r, q, token, user, device := newReportsTestRouterWithQueries(t)
	ctx := context.Background()

	app, err := q.CreateApp(ctx, storedb.CreateAppParams{BundleID: "com.example.reports-enriched." + randomSuffix(t), Platform: "macos", Name: "Enriched App"})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	category := newCategory(t, q, user.ID, "Enriched Category")
	project, err := q.CreateProjectForUser(ctx, storedb.CreateProjectForUserParams{
		ID: newUUIDv4Test(t), UserID: user.ID, Name: "Enriched Project", Color: "#654321",
	})
	if err != nil {
		t.Fatalf("CreateProjectForUser: %v", err)
	}

	day := pastDay(1)
	_, err = q.InsertActivityEvent(ctx, storedb.InsertActivityEventParams{
		EventID: newUUIDv4Test(t), UserID: user.ID, DeviceID: device.ID,
		StartedAt: pgtype.Timestamptz{Time: day.Add(time.Hour), Valid: true},
		EndedAt:   pgtype.Timestamptz{Time: day.Add(time.Hour + time.Minute), Valid: true},
		Type:      "app_active", Source: "desktop", Precision: "exact",
		AppID: app.ID, CategoryID: category.ID, ProjectID: project.ID,
	})
	if err != nil {
		t.Fatalf("InsertActivityEvent: %v", err)
	}

	rec := doReportsReq(t, r, "/v1/reports/timeline?from="+day.Format(time.RFC3339)+"&to="+day.AddDate(0, 0, 1).Format(time.RFC3339), token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data []struct {
			AppID      *string `json:"app_id"`
			CategoryID *string `json:"category_id"`
			ProjectID  *string `json:"project_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Data) != 1 {
		t.Fatalf("len(data) = %d, want 1", len(body.Data))
	}
	if body.Data[0].AppID == nil || body.Data[0].CategoryID == nil || body.Data[0].ProjectID == nil {
		t.Errorf("data[0] = %+v, want app_id/category_id/project_id all set", body.Data[0])
	}
}

func TestHTTP_ReportsTimelineInvalidLimit(t *testing.T) {
	r, token := newReportsTestRouter(t)

	rec := doReportsReq(t, r, "/v1/reports/timeline?from=2024-01-01T00:00:00Z&to=2024-01-02T00:00:00Z&limit=abc", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_WriteUnauthenticatedWithoutMiddleware calls each Handler
// method directly, bypassing the Authenticate middleware, to exercise the
// handler's own defense-in-depth identity check and writeUnauthenticated.
func TestHandler_WriteUnauthenticatedWithoutMiddleware(t *testing.T) {
	q := testQueries(t)
	svc := &Service{Queries: q}
	h := NewHandler(svc)

	tests := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"Summary", h.Summary},
		{"Daily", h.Daily},
		{"Categories", h.Categories},
		{"Apps", h.Apps},
		{"Projects", h.Projects},
		{"Timeline", h.Timeline},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			tt.call(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}
