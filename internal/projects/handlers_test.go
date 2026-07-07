package projects_test

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

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/projects"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

func testQueries(t *testing.T) *storedb.Queries {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping projects HTTP integration test")
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

func newTestRouter(t *testing.T, q *storedb.Queries) (*chi.Mux, *rsa.PrivateKey, string) {
	t.Helper()
	svc := &projects.Service{Queries: q}
	h := projects.NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		projects.RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	return r, key, newUserToken(t, q, key)
}

func newUserToken(t *testing.T, q *storedb.Queries, key *rsa.PrivateKey) string {
	t.Helper()
	suffix := randomSuffix(t)
	user, err := q.CreateUser(context.Background(), storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("projects-http+%s@example.com", suffix)),
		DisplayName: textPtr("Projects HTTP Test User"),
		Role:        "user",
		Timezone:    textPtr("UTC"),
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	t.Cleanup(func() { _ = q.SoftDeleteUser(context.Background(), user.ID) })

	token, err := auth.IssueAccessToken(key, user.ID.String(), "user", "", time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return token
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

// randomUUIDv4 returns a fresh random UUIDv4 string, so tests that need a
// specific (but collision-free across repeated runs against a long-lived
// database) id don't hardcode a literal.
func randomUUIDv4(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

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

func TestHTTP_ProjectsCRUDHappyPath(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	createRec := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{"name": "Website Redesign", "color": "#abcdef"}, token)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create response missing id")
	}

	getRec := doJSON(t, r, http.MethodGet, "/v1/projects/"+id, nil, token)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/v1/projects/", nil, token)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/projects/"+id, map[string]any{"name": "Renamed Project", "archived": true}, token)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patched := decodeBody(t, patchRec)
	if patched["name"] != "Renamed Project" {
		t.Errorf("patched name = %v, want Renamed Project", patched["name"])
	}
	if patched["archived_at"] == nil {
		t.Error("expected archived_at to be set after archiving")
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/projects/"+id, nil, token)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	getAfterDeleteRec := doJSON(t, r, http.MethodGet, "/v1/projects/"+id, nil, token)
	if getAfterDeleteRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404, body = %s", getAfterDeleteRec.Code, getAfterDeleteRec.Body.String())
	}
}

func TestHTTP_ProjectsUnauthenticated(t *testing.T) {
	q := testQueries(t)
	r, _, _ := newTestRouter(t, q)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/v1/projects/"},
		{"create", http.MethodPost, "/v1/projects/"},
		{"get", http.MethodGet, "/v1/projects/00000000-0000-0000-0000-000000000000"},
		{"patch", http.MethodPatch, "/v1/projects/00000000-0000-0000-0000-000000000000"},
		{"delete", http.MethodDelete, "/v1/projects/00000000-0000-0000-0000-000000000000"},
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

func TestHTTP_ProjectsCreateValidationError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{"name": "", "color": "#fff"}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ProjectsCreateInvalidJSONBody(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	req := httptest.NewRequest(http.MethodPost, "/v1/projects/", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ProjectsCreateDuplicateIDConflict(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	id := randomUUIDv4(t)
	first := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{"id": id, "name": "First", "color": "#fff"}, token)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, body = %s", first.Code, first.Body.String())
	}

	second := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{"id": id, "name": "Second", "color": "#fff"}, token)
	if second.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409, body = %s", second.Code, second.Body.String())
	}
}

func TestHTTP_ProjectsGetNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/projects/00000000-0000-0000-0000-000000000000", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ProjectsMalformedIDNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/projects/not-a-uuid", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_ProjectsListInternalError exercises writeServiceError's default
// (500) branch via an already-canceled request context.
func TestHTTP_ProjectsListInternalError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/projects/", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ProjectsListInvalidLimit(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/projects/?limit=not-a-number", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_ProjectsCrossTenant404(t *testing.T) {
	q := testQueries(t)
	r, key, ownerToken := newTestRouter(t, q)
	otherToken := newUserToken(t, q, key)

	createRec := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{"name": "Owner's Project", "color": "#000"}, ownerToken)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/projects/"+id, map[string]any{"name": "hijacked"}, otherToken)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/projects/"+id, nil, otherToken)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_ProjectsPatchFieldCoverage is table-driven coverage for every
// updateRequest field Service.Update accepts (name, color, archived true
// and false), including blank-field rejections.
func TestHTTP_ProjectsPatchFieldCoverage(t *testing.T) {
	tests := []struct {
		name       string
		patch      map[string]any
		wantStatus int
		check      func(t *testing.T, body map[string]any)
	}{
		{
			name:       "update color",
			patch:      map[string]any{"color": "#010101"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["color"] != "#010101" {
					t.Errorf("color = %v, want #010101", body["color"])
				}
			},
		},
		{
			name:       "unarchive",
			patch:      map[string]any{"archived": false},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["archived_at"] != nil {
					t.Errorf("archived_at = %v, want nil after unarchiving", body["archived_at"])
				}
			},
		},
		{
			name:       "blank name rejected",
			patch:      map[string]any{"name": "   "},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "blank color rejected",
			patch:      map[string]any{"color": "   "},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueries(t)
			r, _, token := newTestRouter(t, q)
			createRec := doJSON(t, r, http.MethodPost, "/v1/projects/", map[string]any{
				"name": "Original", "color": "#111111",
			}, token)
			created := decodeBody(t, createRec)
			id, _ := created["id"].(string)

			rec := doJSON(t, r, http.MethodPatch, "/v1/projects/"+id, tt.patch, token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.check != nil {
				tt.check(t, decodeBody(t, rec))
			}
		})
	}
}

// TestHTTP_ProjectsMalformedIDOnPatchAndDelete exercises Update's and
// Delete's parseUUID error branch (ErrNotFound for a malformed id).
func TestHTTP_ProjectsMalformedIDOnPatchAndDelete(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/projects/not-a-uuid", map[string]any{"name": "x"}, token)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/projects/not-a-uuid", nil, token)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHandler_WriteUnauthenticatedWithoutMiddleware calls each Handler
// method directly, bypassing the Authenticate middleware, to exercise the
// handler's own defense-in-depth identity check and writeUnauthenticated.
func TestHandler_WriteUnauthenticatedWithoutMiddleware(t *testing.T) {
	q := testQueries(t)
	svc := &projects.Service{Queries: q}
	h := projects.NewHandler(svc)

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
