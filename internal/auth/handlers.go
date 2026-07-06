package auth

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/httpx"
)

// maxRequestBodyBytes bounds every JSON request body decoded by this
// package's handlers, per documentation/security.md §API hardening
// ("request size limits are enforced on all request bodies").
const maxRequestBodyBytes = 1 << 20 // 1 MiB

const errNS = "https://api.rize-clone.example/errors/"

// decodeJSON decodes r's body into dst, bounding its size and rejecting
// trailing garbage/unknown structure at the top level. It writes an RFC
// 7807-style Problem response and returns false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"invalid-request-body", "Invalid Request Body", "The request body is missing or is not valid JSON.")
		return false
	}
	return true
}

// writeServiceError maps a Service error to the appropriate RFC 7807-style
// Problem response.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", err.Error())
	case errors.Is(err, ErrEmailTaken):
		httpx.WriteError(w, r, http.StatusConflict, errNS+"email-already-registered", "Email Already Registered", "An account with this email already exists.")
	case errors.Is(err, ErrInvalidCredentials):
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"invalid-credentials", "Invalid Credentials", "The email or password provided is incorrect.")
	case errors.Is(err, ErrInvalidRefreshToken):
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"invalid-refresh-token", "Invalid Refresh Token", "The refresh token is missing, unknown, or expired.")
	case errors.Is(err, ErrRefreshTokenReuse):
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"refresh-token-reuse-detected", "Refresh Token Reuse Detected", "This refresh token has already been used; the session has been revoked. Please sign in again.")
	case errors.Is(err, ErrDeviceNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, errNS+"device-not-found", "Device Not Found", "No matching device was found for this account.")
	case errors.Is(err, ErrUserNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, errNS+"user-not-found", "User Not Found", "No matching user was found.")
	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, errNS+"internal-error", "Internal Server Error", "An unexpected error occurred.")
	}
}

// Handler wires the Service's business logic to chi HTTP handlers. Handlers
// stay thin (decode, validate shape, call the service, encode) per
// rize-backend/CLAUDE.md's layering rule; the service is where the actual
// business logic (password verification, token rotation, tenant scoping)
// lives.
type Handler struct {
	Service *Service
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

type registerLoginRequest struct {
	Email    string    `json:"email"`
	Password string    `json:"password"`
	Device   deviceDTO `json:"device"`
}

// Register handles POST /v1/auth/register.
func (h *Handler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	result, err := h.Service.Register(r.Context(), req.Email, req.Password, req.Device.toInput())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusCreated, toAuthResponse(result))
}

// Login handles POST /v1/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req registerLoginRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	result, err := h.Service.Login(r.Context(), req.Email, req.Password, req.Device.toInput())
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, toAuthResponse(result))
}

type refreshRequest struct {
	RefreshToken string     `json:"refresh_token"`
	Device       *deviceDTO `json:"device,omitempty"`
}

// Refresh handles POST /v1/auth/refresh.
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	var req refreshRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	var device *DeviceInput
	if req.Device != nil {
		d := req.Device.toInput()
		device = &d
	}

	result, err := h.Service.Refresh(r.Context(), req.RefreshToken, device)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, toAuthResponse(result))
}

type logoutRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// Logout handles POST /v1/auth/logout (authenticated).
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	var req logoutRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	if err := h.Service.Logout(r.Context(), identity.UserID, req.RefreshToken); err != nil {
		writeServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetMe handles GET /v1/users/me.
func (h *Handler) GetMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	user, err := h.Service.GetProfile(r.Context(), identity.UserID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, userToDTO(user))
}

type updateMeRequest struct {
	DisplayName *string `json:"display_name"`
	Timezone    *string `json:"timezone"`
}

// PatchMe handles PATCH /v1/users/me.
func (h *Handler) PatchMe(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	var req updateMeRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	user, err := h.Service.UpdateProfile(r.Context(), identity.UserID, ProfileUpdate(req))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, userToDTO(user))
}

// ListDevices handles GET /v1/devices.
func (h *Handler) ListDevices(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	devices, err := h.Service.ListDevices(r.Context(), identity.UserID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	dtos := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		dtos = append(dtos, deviceToDTO(d))
	}

	httpx.WriteJSON(w, r, http.StatusOK, map[string]any{"devices": dtos})
}

type patchDeviceRequest struct {
	Name string `json:"name"`
}

// PatchDevice handles PATCH /v1/devices/{id}.
func (h *Handler) PatchDevice(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	var req patchDeviceRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	deviceID := chi.URLParam(r, "id")
	device, err := h.Service.RenameDevice(r.Context(), identity.UserID, deviceID, req.Name)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, deviceToDTO(device))
}

// DeleteDevice handles DELETE /v1/devices/{id}.
func (h *Handler) DeleteDevice(w http.ResponseWriter, r *http.Request) {
	identity, ok := IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	deviceID := chi.URLParam(r, "id")
	if err := h.Service.RevokeDevice(r.Context(), identity.UserID, deviceID); err != nil {
		writeServiceError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
