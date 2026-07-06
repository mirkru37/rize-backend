package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
			router := newRouter(slog.Default(), nil)

			req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
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

func TestReadyzWithoutDatabase(t *testing.T) {
	tests := []struct {
		name       string
		wantStatus int
		wantBody   map[string]string
	}{
		{
			name:       "no DATABASE_URL configured",
			wantStatus: http.StatusOK,
			wantBody:   map[string]string{"status": "ok", "db": "not_configured"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newRouter(slog.Default(), nil)

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
