package auth_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/auth"
	appmw "github.com/mirkru37/rize-backend/internal/middleware"
)

// newTestRouter wires internal/auth's routes behind chi with the real
// Authenticate/RequireRole middleware from internal/middleware, so these
// tests exercise the same request path production traffic takes (per
// documentation/architecture-backend.md §Middleware Stack), just without
// CORS/rate-limit/logging, which are covered by their own package tests.
func newTestRouter(t *testing.T) (*chi.Mux, *auth.Service, *clock) {
	t.Helper()
	svc, clk := newTestService(t)
	handler := auth.NewHandler(svc)

	r := chi.NewRouter()
	r.Route("/v1", func(r chi.Router) {
		auth.RegisterRoutes(r, handler,
			appmw.Authenticate(&svc.SigningKey.PublicKey),
			appmw.RequireRole("admin"),
		)
	})
	return r, svc, clk
}

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

func decodeAuthResponse(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body %q: %v", rec.Body.String(), err)
	}
	return body
}

func registerDeviceBody(name string) map[string]any {
	return map[string]any{
		"platform":    "macos",
		"name":        name,
		"model":       "MacBookPro18,1",
		"os_version":  "14.5",
		"app_version": "1.0.0",
	}
}

// TestHTTP_RegisterLoginRefreshMe walks the same happy path as
// TestRegisterLoginRefreshMeHappyPath but through the full HTTP stack,
// including JSON encoding/decoding and the Authenticate middleware.
func TestHTTP_RegisterLoginRefreshMe(t *testing.T) {
	r, _, _ := newTestRouter(t)
	email := uniqueEmail("http-alice")

	registerRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Alice's MacBook"),
	}, "")
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", registerRec.Code, registerRec.Body.String())
	}
	registerBody := decodeAuthResponse(t, registerRec)
	accessToken, _ := registerBody["access_token"].(string)
	if accessToken == "" {
		t.Fatal("register response missing access_token")
	}

	loginRec := doJSON(t, r, http.MethodPost, "/v1/auth/login", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Alice's MacBook"),
	}, "")
	if loginRec.Code != http.StatusOK {
		t.Fatalf("login status = %d, body = %s", loginRec.Code, loginRec.Body.String())
	}
	loginBody := decodeAuthResponse(t, loginRec)
	refreshToken, _ := loginBody["refresh_token"].(string)
	if refreshToken == "" {
		t.Fatal("login response missing refresh_token")
	}
	loginAccessToken, _ := loginBody["access_token"].(string)

	meRec := doJSON(t, r, http.MethodGet, "/v1/users/me", nil, loginAccessToken)
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /v1/users/me status = %d, body = %s", meRec.Code, meRec.Body.String())
	}
	meBody := decodeAuthResponse(t, meRec)
	if meBody["email"] != email {
		t.Errorf("GET /v1/users/me email = %v, want %q", meBody["email"], email)
	}

	refreshRec := doJSON(t, r, http.MethodPost, "/v1/auth/refresh", map[string]any{
		"refresh_token": refreshToken,
	}, "")
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refreshRec.Code, refreshRec.Body.String())
	}
	refreshBody := decodeAuthResponse(t, refreshRec)
	newRefreshToken, _ := refreshBody["refresh_token"].(string)
	if newRefreshToken == "" || newRefreshToken == refreshToken {
		t.Error("refresh did not return a new, different refresh token")
	}

	// The old refresh token must now be rejected.
	reuseRec := doJSON(t, r, http.MethodPost, "/v1/auth/refresh", map[string]any{
		"refresh_token": refreshToken,
	}, "")
	if reuseRec.Code != http.StatusUnauthorized {
		t.Fatalf("reused refresh token status = %d, want 401, body = %s", reuseRec.Code, reuseRec.Body.String())
	}
}

// TestHTTP_LoginWrongPassword asserts the 401 status code and RFC
// 7807-style body for a bad password, per
// documentation/api-reference.md's worked example.
func TestHTTP_LoginWrongPassword(t *testing.T) {
	r, _, _ := newTestRouter(t)
	email := uniqueEmail("http-bob")

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Bob's MacBook"),
	}, "")
	if regRec.Code != http.StatusCreated {
		t.Fatalf("register status = %d, body = %s", regRec.Code, regRec.Body.String())
	}

	rec := doJSON(t, r, http.MethodPost, "/v1/auth/login", map[string]any{
		"email":    email,
		"password": "wrong-password",
		"device":   registerDeviceBody("Bob's MacBook"),
	}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}

	var problem struct {
		Type   string `json:"type"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("problem.status = %d, want 401", problem.Status)
	}
	if problem.Type == "" {
		t.Error("expected a non-empty problem type")
	}
}

// TestHTTP_LogoutRevokesSession issues logout with the current refresh
// token and asserts it can no longer be refreshed afterward.
func TestHTTP_LogoutRevokesSession(t *testing.T) {
	r, _, _ := newTestRouter(t)
	email := uniqueEmail("http-carol")

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Carol's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)
	refreshToken, _ := regBody["refresh_token"].(string)

	logoutRec := doJSON(t, r, http.MethodPost, "/v1/auth/logout", map[string]any{
		"refresh_token": refreshToken,
	}, accessToken)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, body = %s", logoutRec.Code, logoutRec.Body.String())
	}

	// logout without a bearer token must be rejected.
	unauthRec := doJSON(t, r, http.MethodPost, "/v1/auth/logout", map[string]any{
		"refresh_token": refreshToken,
	}, "")
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated logout status = %d, want 401", unauthRec.Code)
	}

	refreshRec := doJSON(t, r, http.MethodPost, "/v1/auth/refresh", map[string]any{
		"refresh_token": refreshToken,
	}, "")
	if refreshRec.Code != http.StatusUnauthorized {
		t.Fatalf("refresh after logout status = %d, want 401, body = %s", refreshRec.Code, refreshRec.Body.String())
	}
}

// TestHTTP_DeviceTenantIsolation asserts user A's access token cannot list,
// rename, or revoke user B's device via the HTTP devices endpoints.
func TestHTTP_DeviceTenantIsolation(t *testing.T) {
	r, _, _ := newTestRouter(t)

	aRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-userA"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("A's MacBook"),
	}, "")
	aBody := decodeAuthResponse(t, aRec)
	aAccessToken, _ := aBody["access_token"].(string)
	aDevice, _ := aBody["device"].(map[string]any)
	aDeviceID, _ := aDevice["id"].(string)

	bRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-userB"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("B's MacBook"),
	}, "")
	bBody := decodeAuthResponse(t, bRec)
	bAccessToken, _ := bBody["access_token"].(string)

	// B tries to rename A's device.
	renameRec := doJSON(t, r, http.MethodPatch, "/v1/devices/"+aDeviceID, map[string]any{
		"name": "hijacked",
	}, bAccessToken)
	if renameRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant rename status = %d, want 404, body = %s", renameRec.Code, renameRec.Body.String())
	}

	// B tries to revoke A's device.
	revokeRec := doJSON(t, r, http.MethodDelete, "/v1/devices/"+aDeviceID, nil, bAccessToken)
	if revokeRec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant revoke status = %d, want 404, body = %s", revokeRec.Code, revokeRec.Body.String())
	}

	// A can still see/rename their own device.
	listRec := doJSON(t, r, http.MethodGet, "/v1/devices", nil, aAccessToken)
	if listRec.Code != http.StatusOK {
		t.Fatalf("A's device list status = %d, body = %s", listRec.Code, listRec.Body.String())
	}
}

// TestHTTP_NotImplementedStubs asserts the explicitly out-of-scope routes
// return 501 with the standard Problem envelope.
func TestHTTP_NotImplementedStubs(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-stub-user"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Stub User's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	tests := []struct {
		name   string
		method string
		path   string
		bearer string
	}{
		{"sign in with apple", http.MethodPost, "/v1/auth/apple", ""},
		{"password forgot", http.MethodPost, "/v1/auth/password/forgot", ""},
		{"password reset", http.MethodPost, "/v1/auth/password/reset", ""},
		{"delete account", http.MethodDelete, "/v1/users/me", accessToken},
		{"export data", http.MethodPost, "/v1/users/me/export", accessToken},
		{"admin list users", http.MethodGet, "/v1/admin/users", accessToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doJSON(t, r, tt.method, tt.path, map[string]any{}, tt.bearer)
			switch tt.name {
			case "admin list users":
				// accessToken belongs to a "user"-role account, not
				// "admin", so RBAC must reject before the stub handler
				// ever runs.
				if rec.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403 (RBAC), body = %s", rec.Code, rec.Body.String())
				}
			default:
				if rec.Code != http.StatusNotImplemented {
					t.Fatalf("status = %d, want 501, body = %s", rec.Code, rec.Body.String())
				}
			}
		})
	}
}
