package tags_test

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
	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
	"github.com/mirkru37/rize-backend/internal/tags"
)

func testQueries(t *testing.T) *storedb.Queries {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; skipping tags HTTP integration test")
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
	svc := &tags.Service{Queries: q}
	h := tags.NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		tags.RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	return r, key, newUserToken(t, q, key)
}

func newUserToken(t *testing.T, q *storedb.Queries, key *rsa.PrivateKey) string {
	t.Helper()
	suffix := randomSuffix(t)
	user, err := q.CreateUser(context.Background(), storedb.CreateUserParams{
		Email:       textPtr(fmt.Sprintf("tags-http+%s@example.com", suffix)),
		DisplayName: textPtr("Tags HTTP Test User"),
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

func TestHTTP_TagsCRUDHappyPath(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	createRec := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "urgent"}, token)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("create response missing id")
	}

	getRec := doJSON(t, r, http.MethodGet, "/v1/tags/"+id, nil, token)
	if getRec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRec.Code, getRec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/v1/tags/", nil, token)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/tags/"+id, map[string]any{"name": "renamed"}, token)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patched := decodeBody(t, patchRec)
	if patched["name"] != "renamed" {
		t.Errorf("patched name = %v, want renamed", patched["name"])
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/tags/"+id, nil, token)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, body = %s", deleteRec.Code, deleteRec.Body.String())
	}

	getAfterDeleteRec := doJSON(t, r, http.MethodGet, "/v1/tags/"+id, nil, token)
	if getAfterDeleteRec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404, body = %s", getAfterDeleteRec.Code, getAfterDeleteRec.Body.String())
	}
}

func TestHTTP_TagsUnauthenticated(t *testing.T) {
	q := testQueries(t)
	r, _, _ := newTestRouter(t, q)

	tests := []struct {
		name   string
		method string
		path   string
	}{
		{"list", http.MethodGet, "/v1/tags/"},
		{"create", http.MethodPost, "/v1/tags/"},
		{"get", http.MethodGet, "/v1/tags/00000000-0000-0000-0000-000000000000"},
		{"patch", http.MethodPatch, "/v1/tags/00000000-0000-0000-0000-000000000000"},
		{"delete", http.MethodDelete, "/v1/tags/00000000-0000-0000-0000-000000000000"},
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

func TestHTTP_TagsCreateValidationError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "  "}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_TagsCreateInvalidJSONBody(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	req := httptest.NewRequest(http.MethodPost, "/v1/tags/", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_TagsCreateDuplicateNameConflict(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	first := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "duplicate-name"}, token)
	if first.Code != http.StatusCreated {
		t.Fatalf("first create status = %d, body = %s", first.Code, first.Body.String())
	}

	second := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "duplicate-name"}, token)
	if second.Code != http.StatusConflict {
		t.Fatalf("second create status = %d, want 409, body = %s", second.Code, second.Body.String())
	}
}

func TestHTTP_TagsGetNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/tags/00000000-0000-0000-0000-000000000000", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_TagsMalformedIDNotFound(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/tags/not-a-uuid", nil, token)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_TagsListInternalError exercises writeServiceError's default
// (500) branch via an already-canceled request context.
func TestHTTP_TagsListInternalError(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/tags/", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_TagsListInvalidLimit(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	rec := doJSON(t, r, http.MethodGet, "/v1/tags/?limit=not-a-number", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_TagsCrossTenant404(t *testing.T) {
	q := testQueries(t)
	r, key, ownerToken := newTestRouter(t, q)
	otherToken := newUserToken(t, q, key)

	createRec := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "owner-tag"}, ownerToken)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/tags/"+id, map[string]any{"name": "hijacked"}, otherToken)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/tags/"+id, nil, otherToken)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_TagsPatchBlankNameRejected asserts PATCH /v1/tags/{id} rejects
// a blank name (Service.Update's validation branch, not exercised by
// TestHTTP_TagsCRUDHappyPath's non-blank rename).
func TestHTTP_TagsPatchBlankNameRejected(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	createRec := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "original"}, token)
	created := decodeBody(t, createRec)
	id, _ := created["id"].(string)

	rec := doJSON(t, r, http.MethodPatch, "/v1/tags/"+id, map[string]any{"name": "   "}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_TagsMalformedIDOnPatchAndDelete exercises Update's and Delete's
// parseUUID error branch (ErrNotFound for a malformed id).
func TestHTTP_TagsMalformedIDOnPatchAndDelete(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/tags/not-a-uuid", map[string]any{"name": "x"}, token)
	if patchRec.Code != http.StatusNotFound {
		t.Fatalf("patch status = %d, want 404, body = %s", patchRec.Code, patchRec.Body.String())
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/v1/tags/not-a-uuid", nil, token)
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want 404, body = %s", deleteRec.Code, deleteRec.Body.String())
	}
}

// TestHTTP_TagsUpdateDuplicateNameConflict exercises mapConstraintViolation's
// CONFLICT branch via the Update path (TestHTTP_TagsCreateDuplicateNameConflict
// only exercises it via Create).
func TestHTTP_TagsUpdateDuplicateNameConflict(t *testing.T) {
	q := testQueries(t)
	r, _, token := newTestRouter(t, q)

	first := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "first-tag"}, token)
	second := doJSON(t, r, http.MethodPost, "/v1/tags/", map[string]any{"name": "second-tag"}, token)
	secondBody := decodeBody(t, second)
	secondID, _ := secondBody["id"].(string)
	_ = decodeBody(t, first)

	rec := doJSON(t, r, http.MethodPatch, "/v1/tags/"+secondID, map[string]any{"name": "first-tag"}, token)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_WriteUnauthenticatedWithoutMiddleware calls each Handler
// method directly, bypassing the Authenticate middleware, to exercise the
// handler's own defense-in-depth identity check and writeUnauthenticated.
func TestHandler_WriteUnauthenticatedWithoutMiddleware(t *testing.T) {
	q := testQueries(t)
	svc := &tags.Service{Queries: q}
	h := tags.NewHandler(svc)

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
