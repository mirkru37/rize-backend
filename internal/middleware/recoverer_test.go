package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/mirkru37/rize-backend/internal/observability"
)

// fakeSentryTransport is a mocked Sentry transport (RIZ-53: "recoverer
// reports panics to a MOCKED Sentry transport") that records events
// instead of making a network call.
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

func TestRecovererCatchesPanic(t *testing.T) {
	handler := Recoverer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	// Should not panic out of ServeHTTP.
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestRecovererReportsPanicToSentry(t *testing.T) {
	transport := initTestSentry(t)

	handler := Recoverer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}

	events := transport.captured()
	if len(events) != 1 {
		t.Fatalf("captured events = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Message, "boom") {
		t.Errorf("captured event Message = %q, want it to mention the panic value", events[0].Message)
	}
}

func TestRecovererTagsRequestID(t *testing.T) {
	transport := initTestSentry(t)

	router := chimw.RequestID(Recoverer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	events := transport.captured()
	if len(events) != 1 {
		t.Fatalf("captured events = %d, want 1", len(events))
	}
	if got := events[0].Tags["request_id"]; got == "" {
		t.Error("expected request_id tag to be set from chi's RequestID middleware")
	}
}

func TestRecovererStillCallsHandlerNormallyWithoutPanic(t *testing.T) {
	called := false
	handler := Recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if !called {
		t.Error("expected downstream handler to be invoked")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
