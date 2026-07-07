package categories_test

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
	"github.com/mirkru37/rize-backend/internal/categories"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// testQueries returns real sqlc-generated queries backed by DATABASE_URL,
// or skips the test when unset, matching every other package's
// testPool/testQueries helper (see internal/store/store_test.go).
func testQueries(t *testing.T) *storedb.Queries {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping categories HTTP integration test")
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

// newTestRouter wires internal/categories' routes behind chi with the real
// Authenticate middleware from internal/middleware, exercising the same
// request path production traffic takes, and returns an access token for a
// freshly created user. newUserToken can be used to mint tokens for
// additional users against the same router/signing key (e.g. for
// cross-tenant tests).
func newTestRouter(t *testing.T, q *storedb.Queries) (*chi.Mux, *rsa.PrivateKey, string) {
	t.Helper()
	svc := &categories.Service{Queries: q}
	h := categories.NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		categories.RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	return r, key, newUserToken(t, q, key)
}

// newUserToken creates a fresh user and returns a valid access token for
// them, signed with key.
func newUserToken(t *testing.T, q *storedb.Queries, key *rsa.PrivateKey) string {
	t.Helper()
	suffix := randomSuffix(t)
	user, err := q.CreateUser(context.Background(), storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("categories-http+%s@example.com", suffix)),
		DisplayName: textPtr("Categories HTTP Test User"),
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

func TestHTTP_CategoriesCRUDHappyPath(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	createRec := doJSON(t, r, http.MethodPost, "/v1/categories/", map[string]any{
		"name": "Deep Work", "color": "#123456", "productivity": 2,
	}, token)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create response missing id")
	}

	getRec := doJSON(t, r, http.MethodGet, "/v1/categories/"+id, nil, token)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/v1/categories/", nil, token)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/categories/"+id, map[string]any{"name": "Renamed"}, token)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patched := decodeBody(t, patchRec)
	if patched["name"] != "Renamed" {
		t.Errorf("patched name = %v, want Renamed", patched["name"])
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/categories/"+id, nil, token)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	getAfterDeleteRec := doJSON(t, r, http.MethodGet, "/v1/categories/"+id, nil, token)
	if getAfterDeleteRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404, body = %s", getAfterDeleteRec.Code, getAfterDeleteRec.Body.String())
	}
}

func TestHTTP_CategoriesUnauthenticated(t *testing.T) {
	q := testQueries(t)
	r, _, _ := newTestRouter(t, q)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/v1/categories/"},
		{"create", http.MethodPost, "/v1/categories/"},
		{"get", http.MethodGet, "/v1/categories/00000000-0000-0000-0000-000000000000"},
		{"patch", http.MethodPatch, "/v1/categories/00000000-0000-0000-0000-000000000000"},
		{"delete", http.MethodDelete, "/v1/categories/00000000-0000-0000-0000-000000000000"},
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

// TestHTTP_CategoriesCreateBlankFieldsRejected is table-driven coverage
// for Create's blank-name and blank-color validation branches, neither
// reached by TestHTTP_CategoriesCreateValidationError's invalid-productivity
// case.
func TestHTTP_CategoriesCreateBlankFieldsRejected(t *testing.T) {
	tests := []struct {
		name string
		body map[string]any
	}{
		{name: "blank name", body: map[string]any{"name": "   ", "color": "#fff", "productivity": 0}},
		{name: "blank color", body: map[string]any{"name": "Valid", "color": "   ", "productivity": 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueries(t)
			r, _, token := newTestRouter(t, q)

			rec := doJSON(t, r, http.MethodPost, "/v1/categories/", tt.body, token)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHTTP_CategoriesCreateValidationError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodPost, "/v1/categories/", map[string]any{
		"name": "Bad", "color": "#fff", "productivity": 3,
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_CategoriesCreateInvalidJSONBody(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	req := httptest.NewRequest(http.MethodPost, "/v1/categories/", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_CategoriesGetNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/categories/00000000-0000-0000-0000-000000000000", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_CategoriesMalformedIDNotFound asserts a malformed (non-UUID) id
// in the path is reported as 404, exercising parseUUID's error branch —
// distinct from TestHTTP_CategoriesGetNotFound's well-formed-but-unknown
// id, which exercises the DB lookup's not-found branch instead.
func TestHTTP_CategoriesMalformedIDNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/categories/not-a-uuid", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_CategoriesListInternalError exercises writeServiceError's
// default (500) branch: a request whose context is already canceled
// causes the underlying query to fail with an error that isn't one of the
// package's sentinel errors, so it must map to 500 rather than 400/404.
func TestHTTP_CategoriesListInternalError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/categories/", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_CategoriesListInvalidLimit(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/categories/?limit=not-a-number", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_CategoriesCrossTenant404 asserts one user cannot patch/delete
// another user's category via the HTTP endpoints, per
// documentation/security.md §Tenant Isolation.
func TestHTTP_CategoriesCrossTenant404(t *testing.T) {
	q := testQueries(t)
	r, key, ownerToken := newTestRouter(t, q)
	otherToken := newUserToken(t, q, key)

	createRec := doJSON(t, r, http.MethodPost, "/v1/categories/", map[string]any{
		"name": "Owner's", "color": "#000", "productivity": 0,
	}, ownerToken)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/categories/"+id, map[string]any{"name": "hijacked"}, otherToken)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/categories/"+id, nil, otherToken)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_CategoriesPatchFieldCoverage is table-driven coverage for every
// updateRequest field Service.Update accepts (name, color, productivity),
// including their blank/invalid rejections, which
// TestHTTP_CategoriesCRUDHappyPath's single name-only patch doesn't reach.
func TestHTTP_CategoriesPatchFieldCoverage(t *testing.T) {
	tests := []struct {
		name       string
		patch      map[string]any
		wantStatus int
		check      func(t *testing.T, body map[string]any)
	}{
		{
			name:       "update color",
			patch:      map[string]any{"color": "#654321"},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["color"] != "#654321" {
					t.Errorf("color = %v, want #654321", body["color"])
				}
			},
		},
		{
			name:       "update productivity",
			patch:      map[string]any{"productivity": -2},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body map[string]any) {
				if body["productivity"] != float64(-2) {
					t.Errorf("productivity = %v, want -2", body["productivity"])
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
		{
			name:       "invalid productivity rejected",
			patch:      map[string]any{"productivity": 5},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := testQueries(t)
			r, _, token := newTestRouter(t, q)
			createRec := doJSON(t, r, http.MethodPost, "/v1/categories/", map[string]any{
				"name": "Original", "color": "#111111", "productivity": 0,
			}, token)
			created := decodeBody(t, createRec)
			id, _ := created["id"].(string)

			rec := doJSON(t, r, http.MethodPatch, "/v1/categories/"+id, tt.patch, token)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.check != nil {
				tt.check(t, decodeBody(t, rec))
			}
		})
	}
}

// TestHTTP_CategoriesMalformedIDOnPatchAndDelete exercises Update's and
// Delete's parseUUID error branch (ErrNotFound for a malformed id), not
// just Get's.
func TestHTTP_CategoriesMalformedIDOnPatchAndDelete(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/categories/not-a-uuid", map[string]any{"name": "x"}, token)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/categories/not-a-uuid", nil, token)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHandler_WriteUnauthenticatedWithoutMiddleware calls each Handler
// method directly, bypassing the Authenticate middleware entirely (as
// opposed to every other test in this file, which goes through it), to
// exercise the handler's own defense-in-depth identity check and its
// writeUnauthenticated helper.
func TestHandler_WriteUnauthenticatedWithoutMiddleware(t *testing.T) {
	q := testQueries(t)
	svc := &categories.Service{Queries: q}
	h := categories.NewHandler(svc)

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
