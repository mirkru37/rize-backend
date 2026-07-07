package sync

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/httpx"
)

// maxRequestBodyBytes bounds every JSON request body decoded by this
// package's handlers, matching internal/auth's request-size hardening per
// documentation/security.md §API hardening.
const maxRequestBodyBytes = 1 << 20 // 1 MiB

const errNS = "https://api.rize-clone.example/errors/"

// Handler wires Service's business logic to chi HTTP handlers. Handlers
// stay thin (decode, validate shape, call the service, encode) per
// rize-backend/CLAUDE.md's layering rule.
type Handler struct {
	Service *Service
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

// PushEvents handles POST /v1/sync/events, per
// documentation/sync-protocol.md §Push and documentation/api-reference.md
// §Sync.
func (h *Handler) PushEvents(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	var req pushRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"invalid-request-body", "Invalid Request Body", "The request body is missing or is not valid JSON.")
		return
	}

	resp, err := h.Service.push(r.Context(), identity.UserID, req)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, resp)
}

// PullChanges handles GET /v1/sync/changes, per
// documentation/sync-protocol.md §Pull and documentation/api-reference.md
// §Sync.
func (h *Handler) PullChanges(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	cursor := r.URL.Query().Get("cursor")

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", "limit must be a positive integer.")
			return
		}
		limit = parsed
	}

	resp, err := h.Service.pull(r.Context(), identity.UserID, cursor, limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	httpx.WriteJSON(w, r, http.StatusOK, resp)
}

// writeServiceError maps a Service error to the appropriate RFC 7807-style
// Problem response.
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrBatchTooLarge):
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"batch-too-large", "Batch Too Large",
			"A push batch must not contain more than 500 items; split the outbox into sequential batches.")
	case errors.Is(err, ErrDeviceNotFound):
		httpx.WriteError(w, r, http.StatusForbidden, errNS+"device-not-found", "Device Not Found",
			"device_id does not resolve to a device owned by the authenticated user.")
	case errors.Is(err, ErrCursorExpired):
		// 410 Gone: the supplied cursor's position has been pruned from
		// sync_changelog by RIZ-72's age-based retention and can no longer
		// be resumed from. Per documentation/sync-protocol.md §Device
		// Restore from Backup, the client's documented recovery is to
		// reset its cursor to empty and re-pull from the beginning — safe
		// because pulls are idempotent. Gone (rather than a 4xx the client
		// might blindly retry unchanged) signals that this exact cursor
		// value can never succeed again, so the client must supply a
		// different one (empty) rather than retrying as-is.
		httpx.WriteError(w, r, http.StatusGone, errNS+"cursor-expired", "Cursor Expired",
			"The supplied cursor is older than this server's retained change history. Reset your cursor to empty and re-pull from the beginning; pulls are idempotent so this is always safe.")
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", err.Error())
	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, errNS+"internal-error", "Internal Server Error", "An unexpected error occurred.")
	}
}
