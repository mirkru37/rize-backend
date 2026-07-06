package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// chiRouterWithRequestID builds a minimal chi router with the RequestID
// middleware wired in front of the given handler, so tests can verify that
// httpx helpers echo the request ID chi's middleware places in context.
func chiRouterWithRequestID(handler http.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Get("/", handler)
	return r
}

func TestWriteJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	WriteJSON(rec, req, http.StatusOK, map[string]string{"status": "ok"})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("body[status] = %q, want ok", body["status"])
	}
}

func TestWriteJSONEchoesRequestID(t *testing.T) {
	r := chiRouterWithRequestID(func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, r, http.StatusOK, map[string]string{"status": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Header().Get(RequestIDHeader) == "" {
		t.Error("expected X-Request-Id header to be set, got empty")
	}
}

func TestWriteError(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	WriteError(rec, req, http.StatusUnauthorized, "https://api.rize-clone.example/errors/invalid-credentials", "Invalid credentials", "bad token")

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var p Problem
	if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if p.Status != http.StatusUnauthorized {
		t.Errorf("Problem.Status = %d, want %d", p.Status, http.StatusUnauthorized)
	}
	if p.Title != "Invalid credentials" {
		t.Errorf("Problem.Title = %q, want %q", p.Title, "Invalid credentials")
	}
	if p.Detail != "bad token" {
		t.Errorf("Problem.Detail = %q, want %q", p.Detail, "bad token")
	}
	if p.Type != "https://api.rize-clone.example/errors/invalid-credentials" {
		t.Errorf("Problem.Type = %q, want the given type URI", p.Type)
	}
}

func TestWriteErrorEchoesRequestID(t *testing.T) {
	r := chiRouterWithRequestID(func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, http.StatusUnauthorized, "type", "title", "detail")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Header().Get(RequestIDHeader) == "" {
		t.Error("expected X-Request-Id header to be set, got empty")
	}
}
