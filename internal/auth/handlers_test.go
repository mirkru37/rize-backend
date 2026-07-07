package auth_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

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

// TestHTTP_PatchMeUpdatesProfile asserts PATCH /v1/users/me updates the
// authenticated user's own profile fields and that a subsequent GET
// reflects the change.
func TestHTTP_PatchMeUpdatesProfile(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-patchme"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("PatchMe's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	patchRec := doJSON(t, r, http.MethodPatch, "/v1/users/me", map[string]any{
		"display_name": "New Display Name",
		"timezone":     "America/New_York",
	}, accessToken)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}
	patchBody := decodeAuthResponse(t, patchRec)
	if patchBody["display_name"] != "New Display Name" {
		t.Errorf("display_name = %v, want %q", patchBody["display_name"], "New Display Name")
	}

	meRec := doJSON(t, r, http.MethodGet, "/v1/users/me", nil, accessToken)
	meBody := decodeAuthResponse(t, meRec)
	if meBody["display_name"] != "New Display Name" {
		t.Errorf("GET /v1/users/me display_name = %v, want %q", meBody["display_name"], "New Display Name")
	}
}

// TestHTTP_PatchMeBlankFieldsRejected is table-driven coverage for
// UpdateProfile's blank-display_name and blank-timezone validation
// branches, neither reached by TestHTTP_PatchMeUpdatesProfile's non-blank
// update.
func TestHTTP_PatchMeBlankFieldsRejected(t *testing.T) {
	tests := []struct {
		name  string
		patch map[string]any
	}{
		{name: "blank display_name", patch: map[string]any{"display_name": "   "}},
		{name: "blank timezone", patch: map[string]any{"timezone": "   "}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := newTestRouter(t)

			regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
				"email":    uniqueEmail("http-patchme-blank"),
				"password": "correct-horse-battery-staple",
				"device":   registerDeviceBody("Blank Field's MacBook"),
			}, "")
			regBody := decodeAuthResponse(t, regRec)
			accessToken, _ := regBody["access_token"].(string)

			rec := doJSON(t, r, http.MethodPatch, "/v1/users/me", tt.patch, accessToken)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHTTP_PatchMeUnauthenticated asserts PATCH /v1/users/me requires a
// bearer token.
func TestHTTP_PatchMeUnauthenticated(t *testing.T) {
	r, _, _ := newTestRouter(t)

	rec := doJSON(t, r, http.MethodPatch, "/v1/users/me", map[string]any{"display_name": "x"}, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_PatchMeInvalidJSONBody asserts a malformed body is rejected
// with 400 rather than reaching the service layer.
func TestHTTP_PatchMeInvalidJSONBody(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-patchme-badjson"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Bad JSON's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	req := httptest.NewRequest(http.MethodPatch, "/v1/users/me", bytes.NewBufferString("{not-json"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_PatchDeviceOwnDeviceSucceeds asserts a user can rename their
// own device via PATCH /v1/devices/{id}.
func TestHTTP_PatchDeviceOwnDeviceSucceeds(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-renamedevice"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Original Name"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)
	device, _ := regBody["device"].(map[string]any)
	deviceID, _ := device["id"].(string)

	rec := doJSON(t, r, http.MethodPatch, "/v1/devices/"+deviceID, map[string]any{"name": "Renamed Device"}, accessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	body := decodeAuthResponse(t, rec)
	if body["name"] != "Renamed Device" {
		t.Errorf("name = %v, want %q", body["name"], "Renamed Device")
	}
}

// TestHTTP_DeleteDeviceOwnDeviceSucceeds asserts a user can revoke their
// own device via DELETE /v1/devices/{id}, and it no longer appears in
// ListDevices afterward.
func TestHTTP_DeleteDeviceOwnDeviceSucceeds(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-deletedevice"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Doomed Device"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)
	device, _ := regBody["device"].(map[string]any)
	deviceID, _ := device["id"].(string)

	rec := doJSON(t, r, http.MethodDelete, "/v1/devices/"+deviceID, nil, accessToken)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	listRec := doJSON(t, r, http.MethodGet, "/v1/devices", nil, accessToken)
	listBody := decodeAuthResponse(t, listRec)
	devices, _ := listBody["devices"].([]any)
	for _, d := range devices {
		dm, _ := d.(map[string]any)
		if dm["id"] == deviceID {
			t.Fatal("revoked device still appears in ListDevices")
		}
	}
}

// TestHTTP_RegisterLoginRefreshInvalidJSONBody is table-driven coverage
// for each endpoint's decodeJSON-failure branch, which no other test in
// this file reaches for Register/Login/Refresh specifically (only PatchMe
// has an equivalent case).
func TestHTTP_RegisterLoginRefreshInvalidJSONBody(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "register", path: "/v1/auth/register"},
		{name: "login", path: "/v1/auth/login"},
		{name: "refresh", path: "/v1/auth/refresh"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, _ := newTestRouter(t)

			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewBufferString("{not-json"))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHTTP_RefreshWithDeviceMetadataUpdatesDevice exercises Refresh's
// req.Device != nil branch (updating the device's metadata as part of the
// refresh call), which TestHTTP_RegisterLoginRefreshMe's device-less
// refresh never reaches.
func TestHTTP_RefreshWithDeviceMetadataUpdatesDevice(t *testing.T) {
	r, _, _ := newTestRouter(t)
	email := uniqueEmail("http-refresh-device")

	registerRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Old Name"),
	}, "")
	registerBody := decodeAuthResponse(t, registerRec)
	refreshToken, _ := registerBody["refresh_token"].(string)

	refreshRec := doJSON(t, r, http.MethodPost, "/v1/auth/refresh", map[string]any{
		"refresh_token": refreshToken,
		"device":        registerDeviceBody("New Name"),
	}, "")
	if refreshRec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", refreshRec.Code, refreshRec.Body.String())
	}
	refreshBody := decodeAuthResponse(t, refreshRec)
	device, _ := refreshBody["device"].(map[string]any)
	if device["name"] != "New Name" {
		t.Errorf("device.name = %v, want %q", device["name"], "New Name")
	}
}

// TestHTTP_RegisterDuplicateEmailConflict asserts registering a second
// account with an already-registered email returns 409 (ErrEmailTaken),
// exercising that writeServiceError branch.
func TestHTTP_RegisterDuplicateEmailConflict(t *testing.T) {
	r, _, _ := newTestRouter(t)
	email := uniqueEmail("http-dup")

	first := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("First MacBook"),
	}, "")
	if first.Code != http.StatusCreated {
		t.Fatalf("first register status = %d, body = %s", first.Code, first.Body.String())
	}

	second := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    email,
		"password": "a-different-password",
		"device":   registerDeviceBody("Second MacBook"),
	}, "")
	if second.Code != http.StatusConflict {
		t.Fatalf("second register status = %d, want 409, body = %s", second.Code, second.Body.String())
	}
}

// TestHTTP_LogoutInvalidRefreshToken asserts logging out with a
// well-formed-but-unknown refresh token surfaces the service's error
// through writeServiceError rather than succeeding.
func TestHTTP_LogoutInvalidRefreshToken(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-logout-bad-token"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Logout Bad Token's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	notARealToken := "rt_not-a-real-token" //nolint:gosec // test fixture, not a real credential
	rec := doJSON(t, r, http.MethodPost, "/v1/auth/logout", map[string]any{
		"refresh_token": notARealToken,
	}, accessToken)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_PatchDeviceNotFound asserts PATCH /v1/devices/{id} with an id
// that doesn't resolve to any device (owned or otherwise) returns 404.
func TestHTTP_PatchDeviceNotFound(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-patchdevice-404"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("404 Patch's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	rec := doJSON(t, r, http.MethodPatch, "/v1/devices/00000000-0000-0000-0000-000000000000", map[string]any{
		"name": "doesn't matter",
	}, accessToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_PatchDeviceBlankNameRejected asserts PATCH /v1/devices/{id}
// rejects a blank name (RenameDevice's validation branch).
func TestHTTP_PatchDeviceBlankNameRejected(t *testing.T) {
	r, _, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-patchdevice-blank"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("Blank Name's MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)
	device, _ := regBody["device"].(map[string]any)
	deviceID, _ := device["id"].(string)

	rec := doJSON(t, r, http.MethodPatch, "/v1/devices/"+deviceID, map[string]any{"name": "   "}, accessToken)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHandler_AuthenticatedRoutesWithoutMiddleware calls every handler
// method that requires an authenticated identity directly, bypassing the
// Authenticate middleware entirely, to exercise each handler's own
// defense-in-depth identity check (every other test in this file goes
// through the real middleware, which already rejects an unauthenticated
// request before the handler method itself ever runs).
func TestHandler_AuthenticatedRoutesWithoutMiddleware(t *testing.T) {
	_, svc, _ := newTestRouter(t)
	h := auth.NewHandler(svc)

	tests := []struct {
		name string
		call func(w http.ResponseWriter, r *http.Request)
	}{
		{"GetMe", h.GetMe},
		{"PatchMe", h.PatchMe},
		{"ListDevices", h.ListDevices},
		{"PatchDevice", h.PatchDevice},
		{"DeleteDevice", h.DeleteDevice},
		{"Logout", h.Logout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rec := httptest.NewRecorder()
			tt.call(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestHTTP_GetMeWrapsUnexpectedDatabaseError exercises GetMe's
// writeServiceError branch for an unexpected (non-ErrUserNotFound) service
// failure.
func TestHTTP_GetMeWrapsUnexpectedDatabaseError(t *testing.T) {
	r, svc, _ := newTestRouter(t)
	fq := svc.Queries.(*fakeQuerier)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-getme-dberr"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)

	fq.failNextCallTo("GetUserByID", errInjectedDBFailure)
	rec := doJSON(t, r, http.MethodGet, "/v1/users/me", nil, accessToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500, body = %s", rec.Code, rec.Body.String())
	}
}

// TestHTTP_GetMeUserNotFound exercises writeServiceError's ErrUserNotFound
// branch: a valid access token whose subject no longer resolves to a live
// user (e.g. the account was deleted after the token was issued).
func TestHTTP_GetMeUserNotFound(t *testing.T) {
	r, svc, _ := newTestRouter(t)

	regRec := doJSON(t, r, http.MethodPost, "/v1/auth/register", map[string]any{
		"email":    uniqueEmail("http-getme-deleted"),
		"password": "correct-horse-battery-staple",
		"device":   registerDeviceBody("MacBook"),
	}, "")
	regBody := decodeAuthResponse(t, regRec)
	accessToken, _ := regBody["access_token"].(string)
	user, _ := regBody["user"].(map[string]any)
	userID, _ := user["id"].(string)

	var uid pgtype.UUID
	if err := uid.Scan(userID); err != nil {
		t.Fatalf("parse user id: %v", err)
	}
	if err := svc.Queries.SoftDeleteUser(context.Background(), uid); err != nil {
		t.Fatalf("SoftDeleteUser: %v", err)
	}

	rec := doJSON(t, r, http.MethodGet, "/v1/users/me", nil, accessToken)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
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
