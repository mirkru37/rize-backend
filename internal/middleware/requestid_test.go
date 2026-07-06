package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
)

func TestRequestIDSetsIDInContext(t *testing.T) {
	var gotID string

	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = chimw.GetReqID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotID == "" {
		t.Error("expected non-empty request ID in context, got empty")
	}
}

func TestRequestIDPropagatesFromHeader(t *testing.T) {
	var gotID string

	handler := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = chimw.GetReqID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Request-Id", "incoming-request-id")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if gotID == "" {
		t.Fatal("expected non-empty request ID in context, got empty")
	}
	// chi's RequestID middleware appends a per-process prefix, so it
	// won't equal the raw incoming header, but it should incorporate it.
	if !contains(gotID, "incoming-request-id") {
		t.Errorf("gotID = %q, want it to incorporate the incoming X-Request-Id header", gotID)
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
