package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/mirkru37/rize-backend/internal/observability"
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

// fakeSentryTransport is a mocked Sentry transport (RIZ-53) that records
// events instead of making a network call.
type fakeSentryTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *fakeSentryTransport) Configure(sentry.ClientOptions)        {}
func (t *fakeSentryTransport) Flush(time.Duration) bool              { return true }
func (t *fakeSentryTransport) FlushWithContext(context.Context) bool { return true }
func (t *fakeSentryTransport) Close()                                {}

func (t *fakeSentryTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *fakeSentryTransport) captured() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)
	return out
}

func initTestSentry(t *testing.T) *fakeSentryTransport {
	t.Helper()
	transport := &fakeSentryTransport{}
	if err := observability.InitForTesting(observability.Config{
		DSN:         "https://public@example.ingest.sentry.io/1",
		Environment: "test",
	}, transport); err != nil {
		t.Fatalf("observability.InitForTesting: %v", err)
	}
	t.Cleanup(func() { _ = observability.Init(observability.Config{}) })
	return transport
}

// TestWriteErrorReportsToSentryOnlyFor5xx exercises the RIZ-53 /
// documentation/observability.md seam at httpx.WriteError: 5xx responses
// must be reported to Sentry, 4xx responses must not be.
func TestWriteErrorReportsToSentryOnlyFor5xx(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantEvents int
	}{
		{name: "500 is reported", status: http.StatusInternalServerError, wantEvents: 1},
		{name: "503 is reported", status: http.StatusServiceUnavailable, wantEvents: 1},
		{name: "401 is not reported", status: http.StatusUnauthorized, wantEvents: 0},
		{name: "404 is not reported", status: http.StatusNotFound, wantEvents: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := initTestSentry(t)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()

			WriteError(rec, req, tt.status, "type", "title", "detail")

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d", rec.Code, tt.status)
			}
			if got := len(transport.captured()); got != tt.wantEvents {
				t.Fatalf("captured events = %d, want %d", got, tt.wantEvents)
			}
		})
	}
}
