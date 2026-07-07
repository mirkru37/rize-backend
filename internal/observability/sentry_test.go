package observability

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
)

// fakeTransport is a mocked Sentry transport (the boundary
// documentation/observability.md and the RIZ-53 brief call for mocking at)
// that records every event handed to it instead of making a network call.
type fakeTransport struct {
	mu     sync.Mutex
	events []*sentry.Event
}

func (t *fakeTransport) Configure(sentry.ClientOptions)        {}
func (t *fakeTransport) Flush(time.Duration) bool              { return true }
func (t *fakeTransport) FlushWithContext(context.Context) bool { return true }
func (t *fakeTransport) Close()                                {}

func (t *fakeTransport) SendEvent(event *sentry.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, event)
}

func (t *fakeTransport) captured() []*sentry.Event {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]*sentry.Event, len(t.events))
	copy(out, t.events)
	return out
}

// initTestClient wires the global Sentry hub to transport for the duration
// of the test, per the test's own package-level Init/InitForTesting
// contract, and restores a disabled client afterward so later tests (and
// other packages sharing the same test binary) don't observe a
// leftover mock transport.
func initTestClient(t *testing.T, transport sentry.Transport) {
	t.Helper()
	if err := InitForTesting(Config{DSN: "https://public@example.ingest.sentry.io/1", Environment: "test", Release: "test-release"}, transport); err != nil {
		t.Fatalf("InitForTesting: %v", err)
	}
	t.Cleanup(func() {
		_ = Init(Config{})
	})
}

func TestCaptureHTTPError(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantEvents int
	}{
		{name: "5xx is captured", status: 500, wantEvents: 1},
		{name: "502 is captured", status: 502, wantEvents: 1},
		{name: "404 is not captured", status: 404, wantEvents: 0},
		{name: "400 is not captured", status: 400, wantEvents: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			transport := &fakeTransport{}
			initTestClient(t, transport)

			CaptureHTTPError(context.Background(), tt.status, "https://api.example/errors/x", "Title", "detail")

			if got := len(transport.captured()); got != tt.wantEvents {
				t.Fatalf("captured events = %d, want %d", got, tt.wantEvents)
			}
		})
	}
}

func TestCapturePanicReportsToSentry(t *testing.T) {
	transport := &fakeTransport{}
	initTestClient(t, transport)

	CapturePanic(context.Background(), "boom")

	events := transport.captured()
	if len(events) != 1 {
		t.Fatalf("captured events = %d, want 1", len(events))
	}
	if got := events[0].Tags["request_id"]; got != "" {
		t.Errorf("request_id tag = %q, want empty (no request ID in context)", got)
	}
}

func TestInitDisabledIsNoOp(t *testing.T) {
	// DSN-absent is the local-dev/default path: Init must not error, and
	// Capture* calls afterward must not panic or block.
	if err := Init(Config{DSN: "", Environment: "development"}); err != nil {
		t.Fatalf("Init with empty DSN returned error: %v", err)
	}
	t.Cleanup(func() { _ = Init(Config{}) })

	done := make(chan struct{})
	go func() {
		defer close(done)
		CaptureHTTPError(context.Background(), 500, "type", "title", "detail")
		CapturePanic(context.Background(), "boom")
		Flush(100 * time.Millisecond)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("disabled Sentry path blocked instead of returning promptly")
	}
}

func TestScrubEventStripsPII(t *testing.T) {
	event := &sentry.Event{
		Message: "reported by user@example.com",
		User: sentry.User{
			ID:       "user-uuid-1",
			Email:    "user@example.com",
			Username: "jdoe",
			Name:     "Jane Doe",
		},
		Request: &sentry.Request{
			URL:         "https://api.example.com/v1/reports?token=secret",
			QueryString: "token=secret",
			Method:      "POST",
			Data:        `{"password":"hunter2"}`,
			Cookies:     "session=abc123",
			Headers: map[string]string{
				"Authorization": "Bearer abc123",
				"Cookie":        "session=abc123",
				"X-Auth-Token":  "abc123",
				"Content-Type":  "application/json",
			},
		},
		Exception: []sentry.Exception{
			{Value: "failed for contact@rizeclone.example"},
		},
		Contexts: map[string]sentry.Context{
			"extra": {
				"window_title": "Secret Document.pdf",
				"url":          "https://internal.example/secret",
				"note":         "escalate to admin@rizeclone.example",
				"safe":         "no PII here",
			},
		},
		Breadcrumbs: []*sentry.Breadcrumb{
			{
				Message: "navigated, contact owner@rizeclone.example",
				Data: map[string]interface{}{
					"window_title": "Secret Document.pdf",
					"url":          "https://internal.example/secret",
					"note":         "cc finance@rizeclone.example",
				},
			},
		},
	}

	got := scrubEvent(event, nil)

	if got.User.Email != "" {
		t.Errorf("User.Email = %q, want empty", got.User.Email)
	}
	if got.User.Username != "" {
		t.Errorf("User.Username = %q, want empty", got.User.Username)
	}
	if got.User.Name != "" {
		t.Errorf("User.Name = %q, want empty", got.User.Name)
	}
	if got.User.ID != "user-uuid-1" {
		t.Errorf("User.ID = %q, want it preserved (pseudonymous UUID)", got.User.ID)
	}

	if got.Request.URL != "" {
		t.Errorf("Request.URL = %q, want stripped", got.Request.URL)
	}
	if got.Request.QueryString != "" {
		t.Errorf("Request.QueryString = %q, want stripped", got.Request.QueryString)
	}
	if got.Request.Data != "" {
		t.Errorf("Request.Data = %q, want stripped (never send request bodies)", got.Request.Data)
	}
	if got.Request.Cookies != "" {
		t.Errorf("Request.Cookies = %q, want stripped", got.Request.Cookies)
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Auth-Token"} {
		if _, ok := got.Request.Headers[h]; ok {
			t.Errorf("Request.Headers[%q] still present, want stripped", h)
		}
	}
	if got.Request.Headers["Content-Type"] != "application/json" {
		t.Errorf("Request.Headers[Content-Type] = %q, want preserved (not sensitive)", got.Request.Headers["Content-Type"])
	}

	if strings.Contains(got.Message, "@") {
		t.Errorf("Message = %q, want email redacted", got.Message)
	}
	if strings.Contains(got.Exception[0].Value, "@") {
		t.Errorf("Exception.Value = %q, want email redacted", got.Exception[0].Value)
	}

	extra := got.Contexts["extra"]
	if _, ok := extra["window_title"]; ok {
		t.Error(`Contexts["extra"]["window_title"] still present, want stripped`)
	}
	if _, ok := extra["url"]; ok {
		t.Error(`Contexts["extra"]["url"] still present, want stripped`)
	}
	if s, _ := extra["note"].(string); strings.Contains(s, "@") {
		t.Errorf(`Contexts["extra"]["note"] = %q, want email redacted`, s)
	}
	if extra["safe"] != "no PII here" {
		t.Errorf(`Contexts["extra"]["safe"] = %v, want preserved unchanged`, extra["safe"])
	}

	b := got.Breadcrumbs[0]
	if strings.Contains(b.Message, "@") {
		t.Errorf("Breadcrumb.Message = %q, want email redacted", b.Message)
	}
	if _, ok := b.Data["window_title"]; ok {
		t.Error("Breadcrumb.Data[window_title] still present, want stripped")
	}
	if _, ok := b.Data["url"]; ok {
		t.Error("Breadcrumb.Data[url] still present, want stripped")
	}
	if s, _ := b.Data["note"].(string); strings.Contains(s, "@") {
		t.Errorf("Breadcrumb.Data[note] = %q, want email redacted", s)
	}
}

func TestScrubEventNilEvent(t *testing.T) {
	if got := scrubEvent(nil, nil); got != nil {
		t.Errorf("scrubEvent(nil, nil) = %v, want nil", got)
	}
}

func TestIsSensitiveHeader(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"Authorization", true},
		{"authorization", true},
		{"Cookie", true},
		{"Set-Cookie", true},
		{"Proxy-Authorization", true},
		{"X-Auth-Token", true},
		{"X-Api-Token", true},
		{"Content-Type", false},
		{"Accept", false},
		{"X-Request-Id", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSensitiveHeader(tt.name); got != tt.want {
				t.Errorf("isSensitiveHeader(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}
