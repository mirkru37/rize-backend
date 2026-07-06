package categories

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/httpx"
)

const maxRequestBodyBytes = 1 << 20 // 1 MiB

const errNS = "https://api.rize-clone.example/errors/"

// Handler wires Service's business logic to chi HTTP handlers.
type Handler struct {
	Service *Service
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

// List handles the list endpoint for this route group.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", "limit must be a positive integer.")
			return
		}
		limit = parsed
	}

	items, nextCursor, hasMore, err := h.Service.List(r.Context(), identity.UserID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	dtos := make([]categoryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toDTO(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, listResponse{Data: dtos, NextCursor: nextCursor, HasMore: hasMore})
}

// Create handles the create endpoint for this route group.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}

	var req createRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"invalid-request-body", "Invalid Request Body", "The request body is missing or is not valid JSON.")
		return
	}

	created, err := h.Service.Create(r.Context(), identity.UserID, req)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusCreated, toDTO(created))
}

// Get handles the get-by-id endpoint for this route group.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	category, err := h.Service.Get(r.Context(), identity.UserID, chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, toDTO(category))
}

// Patch handles the partial-update endpoint for this route group.
func (h *Handler) Patch(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}

	var req updateRequest
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"invalid-request-body", "Invalid Request Body", "The request body is missing or is not valid JSON.")
		return
	}

	updated, err := h.Service.Update(r.Context(), identity.UserID, chi.URLParam(r, "id"), req)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, toDTO(updated))
}

// Delete handles the delete endpoint for this route group.
func (h *Handler) Delete(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	if err := h.Service.Delete(r.Context(), identity.UserID, chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, r, http.StatusNotFound, errNS+"category-not-found", "Category Not Found",
			"No editable category with that id exists for the authenticated user.")
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", err.Error())
	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, errNS+"internal-error", "Internal Server Error", "An unexpected error occurred.")
	}
}
