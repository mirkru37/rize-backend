package activities_test

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/activities"
	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// newHTTPTestRouter wires internal/activities' route behind chi with the
// real Authenticate middleware, reusing testQueries/newUserAndDevices from
// service_test.go, and returns the signing key (so a caller can mint
// tokens for additional users against the same router, e.g. for
// cross-tenant tests) plus an access token for the created user.
func newHTTPTestRouter(t *testing.T, q *storedb.Queries) (*chi.Mux, *rsa.PrivateKey, string, storedb.User, storedb.Device) {
	t.Helper()
	svc := &activities.Service{Queries: q}
	h := activities.NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		activities.RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	token, user, device := newUserTokenForKey(t, q, key)
	return r, key, token, user, device
}

func newUserTokenForKey(t *testing.T, q *storedb.Queries, key *rsa.PrivateKey) (string, storedb.User, storedb.Device) {
	t.Helper()
	user, device, _ := newUserAndDevices(t, q)
	token, err := auth.IssueAccessToken(key, user.ID.String(), "user", device.ID.String(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return token, user, device
}

func doReq(t *testing.T, r http.Handler, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestHTTP_ActivitiesListHappyPath(t *testing.T) {
	q := testQueries(t)
	r, _, token, user, device := newHTTPTestRouter(t, q)

	base := time.Date(2021, 1, 1, 9, 0, 0, 0, time.UTC)
	insertEvent(t, q, user.ID, device.ID, base, base.Add(10*time.Minute), "exact")

	path := fmt.Sprintf("/v1/activities?from=%s&to=%s",
		base.Add(-time.Hour).Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	rec := doReq(t, r, path, token)
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

// TestHTTP_ActivitiesListEnrichedEvent seeds an event with app_id,
// category_id, and project_id all set, exercising toDTO's enrichment
// branches (AppID/CategoryID/ProjectID.Valid), which every other event
// inserted by this file's tests (via insertEvent, which never sets those
// columns) leaves unexercised.
func TestHTTP_ActivitiesListEnrichedEvent(t *testing.T) {
	q := testQueries(t)
	r, _, token, user, device := newHTTPTestRouter(t, q)
	ctx := context.Background()

	app, err := q.CreateApp(ctx, storedb.CreateAppParams{BundleID: "com.example.enriched." + randomSuffix(t), Platform: "macos", Name: "Enriched App"})
	if err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	category, err := q.CreateCategoryForUser(ctx, storedb.CreateCategoryForUserParams{
		UserID: user.ID, Name: "Enriched Category", Color: "#123456", Productivity: 1,
	})
	if err != nil {
		t.Fatalf("CreateCategoryForUser: %v", err)
	}
	projectID, err := newUUIDv4Test()
	if err != nil {
		t.Fatalf("newUUIDv4: %v", err)
	}
	project, err := q.CreateProjectForUser(ctx, storedb.CreateProjectForUserParams{
		ID: projectID, UserID: user.ID, Name: "Enriched Project", Color: "#654321",
	})
	if err != nil {
		t.Fatalf("CreateProjectForUser: %v", err)
	}

	base := time.Date(2022, 6, 1, 9, 0, 0, 0, time.UTC)
	eventID, err := newUUIDv4Test()
	if err != nil {
		t.Fatalf("newUUIDv4: %v", err)
	}
	_, err = q.InsertActivityEvent(ctx, storedb.InsertActivityEventParams{
		EventID: eventID, UserID: user.ID, DeviceID: device.ID,
		StartedAt: pgtype.Timestamptz{Time: base, Valid: true},
		EndedAt:   pgtype.Timestamptz{Time: base.Add(10 * time.Minute), Valid: true},
		Type:      "app_active", Source: "desktop", Precision: "exact",
		AppID: app.ID, CategoryID: category.ID, ProjectID: project.ID,
	})
	if err != nil {
		t.Fatalf("InsertActivityEvent: %v", err)
	}

	path := fmt.Sprintf("/v1/activities?from=%s&to=%s",
		base.Add(-time.Hour).Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	rec := doReq(t, r, path, token)
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
	if body.Data[0].AppID == nil || *body.Data[0].AppID != app.ID.String() {
		t.Errorf("app_id = %v, want %q", body.Data[0].AppID, app.ID.String())
	}
	if body.Data[0].CategoryID == nil || *body.Data[0].CategoryID != category.ID.String() {
		t.Errorf("category_id = %v, want %q", body.Data[0].CategoryID, category.ID.String())
	}
	if body.Data[0].ProjectID == nil || *body.Data[0].ProjectID != project.ID.String() {
		t.Errorf("project_id = %v, want %q", body.Data[0].ProjectID, project.ID.String())
	}
}

func TestHTTP_ActivitiesListUnauthenticated(t *testing.T) {
	q := testQueries(t)
	r, _, _, _, _ := newHTTPTestRouter(t, q)

	rec := doReq(t, r, "/v1/activities?from=2021-01-01T00:00:00Z&to=2021-01-02T00:00:00Z", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ActivitiesListMissingFromTo(t *testing.T) {
	q := testQueries(t)
	r, _, token, _, _ := newHTTPTestRouter(t, q)

	rec := doReq(t, r, "/v1/activities", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ActivitiesListInvalidTimeFormat(t *testing.T) {
	q := testQueries(t)
	r, _, token, _, _ := newHTTPTestRouter(t, q)

	rec := doReq(t, r, "/v1/activities?from=not-a-time&to=2021-01-02T00:00:00Z", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ActivitiesListInvalidFilters is table-driven coverage for
// every optional filter's UUID-validation branch (app_id, category_id,
// project_id, device_id) plus an invalid precision value and a tampered
// cursor, none of which TestHTTP_ActivitiesListHappyPath's filter-less
// request reaches.
func TestHTTP_ActivitiesListInvalidFilters(t *testing.T) {
	tests := []struct {
		name  string
		query string
	}{
		{name: "invalid app_id", query: "&app_id=not-a-uuid"},
		{name: "invalid category_id", query: "&category_id=not-a-uuid"},
		{name: "invalid project_id", query: "&project_id=not-a-uuid"},
		{name: "invalid device_id", query: "&device_id=not-a-uuid"},
		{name: "invalid precision", query: "&precision=bogus"},
		{name: "tampered cursor", query: "&cursor=not-a-valid-cursor!!"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueries(t)
			r, _, token, _, _ := newHTTPTestRouter(t, q)

			rec := doReq(t, r, "/v1/activities?from=2021-01-01T00:00:00Z&to=2021-01-02T00:00:00Z"+tt.query, token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHTTP_ActivitiesListInvalidLimit(t *testing.T) {
	q := testQueries(t)
	r, _, token, _, _ := newHTTPTestRouter(t, q)

	rec := doReq(t, r, "/v1/activities?from=2021-01-01T00:00:00Z&to=2021-01-02T00:00:00Z&limit=abc", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ActivitiesListAbsurdRangeRejected(t *testing.T) {
	q := testQueries(t)
	r, _, token, _, _ := newHTTPTestRouter(t, q)

	rec := doReq(t, r, "/v1/activities?from=2000-01-01T00:00:00Z&to=2026-01-01T00:00:00Z", token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ActivitiesListInternalError exercises writeServiceError's
// default (500) branch via an already-canceled request context.
func TestHTTP_ActivitiesListInternalError(t *testing.T) {
	q := testQueries(t)
	r, _, token, _, _ := newHTTPTestRouter(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/activities?from=2021-01-01T00:00:00Z&to=2021-01-02T00:00:00Z", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ActivitiesListTenantIsolation(t *testing.T) {
	q := testQueries(t)
	rA, key, _, userA, deviceA := newHTTPTestRouter(t, q)
	tokenB, _, _ := newUserTokenForKey(t, q, key)

	base := time.Date(2021, 2, 1, 9, 0, 0, 0, time.UTC)
	insertEvent(t, q, userA.ID, deviceA.ID, base, base.Add(10*time.Minute), "exact")

	path := fmt.Sprintf("/v1/activities?from=%s&to=%s",
		base.Add(-time.Hour).Format(time.RFC3339), base.Add(time.Hour).Format(time.RFC3339))
	rec := doReq(t, rA, path, tokenB)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(body.Data) != 0 {
		t.Fatalf("len(data) = %d, want 0 (userB must not see userA's events)", len(body.Data))
	}
}
