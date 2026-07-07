package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// newSyncTestRouter wires this package's push/pull routes behind chi with
// the real Authenticate middleware, and returns an access token for a
// freshly created user + device (reusing newUser from service_test.go).
func newSyncTestRouter(t *testing.T) (*chi.Mux, storedb.User, storedb.Device, string) {
	t.Helper()
	pool := testPool(t)
	q := storedb.New(pool)
	svc := &Service{Queries: q, Pool: pool}
	h := NewHandler(svc)

	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		RegisterRoutes(r, h, appmw.Authenticate(&key.PublicKey))
	})

	user, device := newUser(t, q)
	token, err := auth.IssueAccessToken(key, user.ID.String(), "user", device.ID.String(), time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	return r, user, device, token
}

func doSyncJSON(t *testing.T, r http.Handler, method, path string, body any, bearer string) *httptest.ResponseRecorder {
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

func TestHTTP_SyncPushEventsHappyPath(t *testing.T) {
	r, _, device, token := newSyncTestRouter(t)

	now := time.Now().UTC().Truncate(time.Second)
	body := map[string]any{
		"device_id": device.ID.String(),
		"items": []map[string]any{
			{
				"entity_type": "activity_event",
				"data": map[string]any{
					"event_id":      newUUIDv7(t),
					"started_at":    now.Format(time.RFC3339),
					"ended_at":      now.Add(time.Minute).Format(time.RFC3339),
					"app_bundle_id": "com.example.app",
					"precision":     "exact",
					"deleted":       false,
				},
			},
		},
	}

	rec := doSyncJSON(t, r, http.MethodPost, "/v1/sync/events", body, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0]["status"] != "applied" {
		t.Fatalf("results = %+v, want exactly one applied result", resp.Results)
	}
}

func TestHTTP_SyncPushEventsUnauthenticated(t *testing.T) {
	r, _, device, _ := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodPost, "/v1/sync/events", map[string]any{
		"device_id": device.ID.String(), "items": []map[string]any{},
	}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPushEventsInvalidJSONBody(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/sync/events", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPushEventsUnknownDeviceRejected(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodPost, "/v1/sync/events", map[string]any{
		"device_id": "00000000-0000-0000-0000-000000000000",
		"items":     []map[string]any{},
	}, token)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPushEventsBatchTooLarge(t *testing.T) {
	r, _, device, token := newSyncTestRouter(t)

	items := make([]map[string]any, 501)
	now := time.Now().UTC()
	for i := range items {
		items[i] = map[string]any{
			"entity_type": "activity_event",
			"data": map[string]any{
				"event_id":      newUUIDv7(t),
				"started_at":    now.Format(time.RFC3339),
				"ended_at":      now.Add(time.Minute).Format(time.RFC3339),
				"app_bundle_id": "com.example.app",
				"precision":     "exact",
				"deleted":       false,
			},
		}
	}

	rec := doSyncJSON(t, r, http.MethodPost, "/v1/sync/events", map[string]any{
		"device_id": device.ID.String(), "items": items,
	}, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPullChangesHappyPath(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodGet, "/v1/sync/changes", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_SyncPullChangesInternalError exercises writeServiceError's
// default (500) branch via an already-canceled request context.
func TestHTTP_SyncPullChangesInternalError(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/sync/changes", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPullChangesUnauthenticated(t *testing.T) {
	r, _, _, _ := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodGet, "/v1/sync/changes", nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPullChangesInvalidLimit(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodGet, "/v1/sync/changes?limit=abc", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_SyncPullChangesTamperedCursorRejected(t *testing.T) {
	r, _, _, token := newSyncTestRouter(t)

	rec := doSyncJSON(t, r, http.MethodGet, "/v1/sync/changes?cursor=not-a-valid-cursor!!", nil, token)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}
