package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mirkru37/rize-backend/internal/auth"
)

func TestAuthenticate(t *testing.T) {
	key, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey: %v", err)
	}
	otherKey, err := auth.GenerateSigningKey()
	if err != nil {
		t.Fatalf("GenerateSigningKey (otherKey): %v", err)
	}

	validToken, err := auth.IssueAccessToken(key, "user-123", "user", "device-abc", time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken: %v", err)
	}
	expiredToken, err := auth.IssueAccessToken(key, "user-123", "user", "device-abc", time.Now().Add(-2*auth.AccessTokenTTL))
	if err != nil {
		t.Fatalf("IssueAccessToken (expired): %v", err)
	}
	wrongKeyToken, err := auth.IssueAccessToken(otherKey, "user-123", "user", "device-abc", time.Now())
	if err != nil {
		t.Fatalf("IssueAccessToken (wrong key): %v", err)
	}

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{name: "missing Authorization header", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "missing Bearer prefix", authHeader: validToken, wantStatus: http.StatusUnauthorized},
		{name: "empty bearer token", authHeader: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "malformed token", authHeader: "Bearer not-a-jwt", wantStatus: http.StatusUnauthorized},
		{name: "expired token", authHeader: "Bearer " + expiredToken, wantStatus: http.StatusUnauthorized},
		{name: "token signed with a different key", authHeader: "Bearer " + wrongKeyToken, wantStatus: http.StatusUnauthorized},
		{name: "valid token", authHeader: "Bearer " + validToken, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			var gotIdentity auth.Identity
			var gotOK bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotIdentity, gotOK = auth.IdentityFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			handler := Authenticate(&key.PublicKey)(next)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus == http.StatusOK {
				if !gotOK {
					t.Fatal("expected an identity to be attached to the request context")
				}
				if gotIdentity.UserID != "user-123" || gotIdentity.Role != "user" || gotIdentity.DeviceID != "device-abc" {
					t.Errorf("identity = %+v, want UserID=user-123 Role=user DeviceID=device-abc", gotIdentity)
				}
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	tests := []struct {
		name         string
		withIdentity bool
		role         string
		requiredRole string
		wantStatus   int
	}{
		{name: "no identity in context", withIdentity: false, wantStatus: http.StatusUnauthorized},
		{name: "matching role", withIdentity: true, role: "admin", requiredRole: "admin", wantStatus: http.StatusOK},
		{name: "non-matching role", withIdentity: true, role: "user", requiredRole: "admin", wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			called := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			})

			handler := RequireRole(tt.requiredRole)(next)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.withIdentity {
				ctx := auth.WithIdentity(req.Context(), auth.Identity{UserID: "u1", Role: tt.role})
				req = req.WithContext(ctx)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			wantCalled := tt.wantStatus == http.StatusOK
			if called != wantCalled {
				t.Errorf("next handler called = %v, want %v", called, wantCalled)
			}
		})
	}
}
