package focussessions_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/focussessions"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

func testQueries(t *testing.T) *storedb.Queries {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping focussessions HTTP integration test")
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

func newTestRouter(t *testing.T, q *storedb.Queries) (*chi.Mux, *rsa.PrivateKey, string, string) {
	t.Helper()
	svc := &focussessions.Service{Queries: q}
	h := focussessions.NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		focussessions.RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	token, deviceID := newUserTokenAndDevice(t, q, key)
	return r, key, token, deviceID
}

// newUserTokenAndDevice creates a fresh user and one of their devices,
// returning an access token for the user and the device's id (focus
// sessions require a device_id owned by the authenticated user).
func newUserTokenAndDevice(t *testing.T, q *storedb.Queries, key *rsa.PrivateKey) (token, deviceID string) {
	t.Helper()
	suffix := randomSuffix(t)
	ctx := context.Background()
	user, err := q.CreateUser(ctx, storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("focussessions-http+%s@example.com", suffix)),
		DisplayName: textPtr("FocusSessions HTTP Test User"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(ctx, user.ID) })

	device, err := q.CreateDevice(ctx, storedb.CreateDeviceParams{
		UserID: user.ID, Platform: "macos", Name: "test-device", Model: "m", OsVersion: "1", AppVersion: "1",
	})
	if err != nil {
		t.Fatalf("CreateDevice: %v", err)
	}

	token, err = auth.IssueAccessToken(key, user.ID.String(), "user", device.ID.String(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return token, device.ID.String()
}

// parseUUIDForTest converts a string user id (as carried in an access
// token's "sub" claim) back into the pgtype.UUID storedb params expect.
func parseUUIDForTest(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

// randomUUIDv4ForTest returns a fresh random UUIDv4, for storedb params
// (e.g. CreateProjectForUserParams.ID) that require an explicit id.
func randomUUIDv4ForTest(t *testing.T) pgtype.UUID {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}
}

func randomSuffix(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	return hex.EncodeToString(b)
}

func textPtr(s string) *string { return &s }

func doJSON(t *testing.T, r http.Handler, method, path string, body any, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode request body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestHTTP_FocusSessionsCRUDHappyPath(t *testing.T) {
	q := testQueries(t)
	r, _, token, deviceID := newTestRouter(t, q)

	createRec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", map[string]any{
		"device_id":  deviceID,
		"kind":       "focus",
		"status":     "running",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}, token)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create response missing id")
	}

	getRec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/"+id, nil, token)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/", nil, token)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/focus-sessions/"+id, map[string]any{"status": "completed"}, token)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patched := decodeBody(t, patchRec)
	if patched["status"] != "completed" {
		t.Errorf("patched status = %v, want completed", patched["status"])
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/focus-sessions/"+id, nil, token)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	getAfterDeleteRec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/"+id, nil, token)
	if getAfterDeleteRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404, body = %s", getAfterDeleteRec.Code, getAfterDeleteRec.Body.String())
	}
}

func TestHTTP_FocusSessionsUnauthenticated(t *testing.T) {
	q := testQueries(t)
	r, _, _, _ := newTestRouter(t, q)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/v1/focus-sessions/"},
		{"create", http.MethodPost, "/v1/focus-sessions/"},
		{"get", http.MethodGet, "/v1/focus-sessions/00000000-0000-0000-0000-000000000000"},
		{"patch", http.MethodPatch, "/v1/focus-sessions/00000000-0000-0000-0000-000000000000"},
		{"delete", http.MethodDelete, "/v1/focus-sessions/00000000-0000-0000-0000-000000000000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doJSON(t, r, tt.method, tt.path, map[string]any{}, "")
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHTTP_FocusSessionsCreateFieldValidation is table-driven coverage for
// Create's remaining validation branches (malformed device_id, malformed
// and not-owned project_id, invalid status, invalid started_at/ended_at),
// beyond TestHTTP_FocusSessionsCreateValidationError's invalid-kind case.
func TestHTTP_FocusSessionsCreateFieldValidation(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339)

	tests := []struct {
		name   string
		mutate func(body map[string]any)
	}{
		{name: "malformed device_id", mutate: func(b map[string]any) { b["device_id"] = "not-a-uuid" }},
		{name: "malformed project_id", mutate: func(b map[string]any) { b["project_id"] = "not-a-uuid" }},
		{name: "not-owned project_id", mutate: func(b map[string]any) { b["project_id"] = "00000000-0000-0000-0000-000000000000" }},
		{name: "invalid status", mutate: func(b map[string]any) { b["status"] = "not-a-real-status" }},
		{name: "invalid started_at", mutate: func(b map[string]any) { b["started_at"] = "not-a-timestamp" }},
		{name: "invalid ended_at", mutate: func(b map[string]any) { b["ended_at"] = "not-a-timestamp" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueries(t)
			r, _, token, deviceID := newTestRouter(t, q)

			body := map[string]any{
				"device_id":  deviceID,
				"kind":       "focus",
				"status":     "running",
				"started_at": now,
			}
			tt.mutate(body)

			rec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", body, token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHTTP_FocusSessionsCreateValidationError(t *testing.T) {
	q := testQueries(t)
	r, _, token, deviceID := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", map[string]any{
		"device_id":  deviceID,
		"kind":       "not-a-valid-kind",
		"status":     "running",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_FocusSessionsCreateInvalidJSONBody(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	req := httptest.NewRequest(http.MethodPost, "/v1/focus-sessions/", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_FocusSessionsGetNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/00000000-0000-0000-0000-000000000000", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_FocusSessionsMalformedIDNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/not-a-uuid", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_FocusSessionsMalformedIDOnPatchAndDelete exercises Update's
// and Delete's parseUUID error branch (ErrNotFound for a malformed id).
func TestHTTP_FocusSessionsMalformedIDOnPatchAndDelete(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/focus-sessions/not-a-uuid", map[string]any{"status": "completed"}, token)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/focus-sessions/not-a-uuid", nil, token)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_FocusSessionsListInternalError exercises writeServiceError's
// default (500) branch via an already-canceled request context.
func TestHTTP_FocusSessionsListInternalError(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/focus-sessions/", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_FocusSessionsListInvalidLimit(t *testing.T) {
	q := testQueries(t)
	r, _, token, _ := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/focus-sessions/?limit=not-a-number", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_FocusSessionsCrossTenant404(t *testing.T) {
	q := testQueries(t)
	r, key, ownerToken, deviceID := newTestRouter(t, q)
	otherToken, _ := newUserTokenAndDevice(t, q, key)

	createRec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", map[string]any{
		"device_id":  deviceID,
		"kind":       "focus",
		"status":     "running",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}, ownerToken)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/focus-sessions/"+id, map[string]any{"status": "abandoned"}, otherToken)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/focus-sessions/"+id, nil, otherToken)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_FocusSessionsPatchFieldCoverage is table-driven coverage for
// Service.Update's remaining fields (device_id, project_id/clear,
// planned_duration_s, started_at, ended_at/clear, note, and the kind/status
// validation branches), which TestHTTP_FocusSessionsCRUDHappyPath's
// status-only patch doesn't reach.
func TestHTTP_FocusSessionsPatchFieldCoverage(t *testing.T) {
	q := testQueries(t)
	r, _, token, deviceID := newTestRouter(t, q)

	newSession := func(t *testing.T) string {
		createRec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", map[string]any{
			"device_id":  deviceID,
			"kind":       "focus",
			"status":     "running",
			"started_at": time.Now().UTC().Format(time.RFC3339),
			"note":       "original note",
		}, token)
		if createRec.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
		}
		created := decodeBody(t, createRec)
		id, _ := created["id"].(string)
		return id
	}

	tests := []struct {
		name       string
		patch      map[string]any
		wantStatus int
		check      func(t *testing.T, body map[string]any)
	}{
		{
			name:       "update kind",
			patch:      map[string]any{"kind": "break"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["kind"] != "break" {
					t.Errorf("kind = %v, want break", body["kind"])
				}
			},
		},
		{
			name:       "update planned_duration_s",
			patch:      map[string]any{"planned_duration_s": 900},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["planned_duration_s"] != float64(900) {
					t.Errorf("planned_duration_s = %v, want 900", body["planned_duration_s"])
				}
			},
		},
		{
			name:       "update started_at",
			patch:      map[string]any{"started_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
			wantStatus: http.StatusOK,
		},
		{
			name:       "update ended_at",
			patch:      map[string]any{"ended_at": time.Now().UTC().Format(time.RFC3339)},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["ended_at"] == nil {
					t.Error("expected ended_at to be set")
				}
			},
		},
		{
			name:       "clear ended_at",
			patch:      map[string]any{"clear_ended_at": true},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["ended_at"] != nil {
					t.Errorf("ended_at = %v, want nil after clearing", body["ended_at"])
				}
			},
		},
		{
			name:       "update note",
			patch:      map[string]any{"note": "updated note"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["note"] != "updated note" {
					t.Errorf("note = %v, want %q", body["note"], "updated note")
				}
			},
		},
		{
			name:       "update device_id",
			patch:      map[string]any{"device_id": deviceID},
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid kind rejected",
			patch:      map[string]any{"kind": "not-a-real-kind"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid status rejected",
			patch:      map[string]any{"status": "not-a-real-status"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid started_at rejected",
			patch:      map[string]any{"started_at": "not-a-timestamp"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid ended_at rejected",
			patch:      map[string]any{"ended_at": "not-a-timestamp"},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "invalid device_id rejected",
			patch:      map[string]any{"device_id": "00000000-0000-0000-0000-000000000000"},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := newSession(t)
			rec := doJSON(t, r, http.MethodPatch, "/v1/focus-sessions/"+id, tt.patch, token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.check != nil {
				tt.check(t, decodeBody(t, rec))
			}
		})
	}
}

// TestHTTP_FocusSessionsPatchClearProjectID exercises Update's
// ClearProjectID branch: create a session with a project, then clear it.
func TestHTTP_FocusSessionsPatchClearProjectID(t *testing.T) {
	q := testQueries(t)
	r, key, token, deviceID := newTestRouter(t, q)

	// Insert the project directly via storedb rather than HTTP: this
	// package's test router only mounts the /v1/focus-sessions routes, not
	// /v1/projects.
	claims, err := auth.VerifyAccessToken(&key.PublicKey, token)
	if err != nil {
		t.Fatalf("VerifyAccessToken: %v", err)
	}
	uid, err := parseUUIDForTest(claims.Subject)
	if err != nil {
		t.Fatalf("parse user id from token: %v", err)
	}
	projectRow, err := q.CreateProjectForUser(context.Background(), storedb.CreateProjectForUserParams{
		ID: randomUUIDv4ForTest(t), UserID: uid, Name: "P", Color: "#fff",
	})
	if err != nil {
		t.Fatalf("CreateProjectForUser: %v", err)
	}
	projectID := projectRow.ID.String()

	createRec := doJSON(t, r, http.MethodPost, "/v1/focus-sessions/", map[string]any{
		"device_id":  deviceID,
		"project_id": projectID,
		"kind":       "focus",
		"status":     "running",
		"started_at": time.Now().UTC().Format(time.RFC3339),
	}, token)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)
	if created["project_id"] != projectID {
		t.Fatalf("created project_id = %v, want %q", created["project_id"], projectID)
	}

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/focus-sessions/"+id, map[string]any{"clear_project_id": true}, token)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patched := decodeBody(t, patchRec)
	if patched["project_id"] != nil {
		t.Errorf("project_id = %v, want nil after clearing", patched["project_id"])
	}
}

// TestHandler_WriteUnauthenticatedWithoutMiddleware calls each Handler
// method directly, bypassing the Authenticate middleware, to exercise the
// handler's own defense-in-depth identity check and writeUnauthenticated.
func TestHandler_WriteUnauthenticatedWithoutMiddleware(t *testing.T) {
	q := testQueries(t)
	svc := &focussessions.Service{Queries: q}
	h := focussessions.NewHandler(svc)

	tests := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"List", h.List},
		{"Create", h.Create},
		{"Get", h.Get},
		{"Patch", h.Patch},
		{"Delete", h.Delete},
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
