package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mirkru37/rize-backend/internal/config"
)

func testConfig() config.Config {
	return config.Config{
		HTTPPort:                   "8080",
		Environment:                "development",
		CORSAllowedOrigins:         []string{"*"},
		RateLimitRequestsPerMinute: 1000,
		ShutdownTimeout:            10 * time.Second,
		ReadyzDBPingTimeout:        5 * time.Second,
	}
}

func TestHealthz(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name:       "returns ok",
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"status": "ok"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newRouter(slog.Default(), testConfig(), nil)

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if rec.Header().Get("X-Request-Id") == "" {
				t.Error("expected X-Request-Id header to be set")
			}

			var got map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode body: %v", err)
			}

			for k, v := range tt.wantBody {
				if got[k] != v {
					t.Errorf("body[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestReadyzWithoutDatabase(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name:       "no database configured",
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"status": "ok", "db": "not_configured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newRouter(slog.Default(), testConfig(), nil)

			req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
			rec := httptest.NewRecorder()

			router.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}

			var got map[string]string
			if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
				t.Fatalf("decode body: %v", err)
			}

			for k, v := range tt.wantBody {
				if got[k] != v {
					t.Errorf("body[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestMetricsEndpoint(t *testing.T) {
	router := newRouter(slog.Default(), testConfig(), nil)

	// Generate at least one request so the metrics registry has data.
	warmupReq := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	router.ServeHTTP(httptest.NewRecorder(), warmupReq)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "http_requests_total") {
		t.Error("expected http_requests_total in /metrics output")
	}
}

func TestV1MountExists(t *testing.T) {
	router := newRouter(slog.Default(), testConfig(), nil)

	req := httptest.NewRequest(http.MethodGet, "/v1/does-not-exist", nil)
	rec := httptest.NewRecorder()

	// Should route through the /v1 subrouter (with CORS + rate-limit
	// middleware applied) without panicking, and 404 since no business
	// routes are registered yet.
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
