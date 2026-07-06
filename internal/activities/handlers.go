package activities

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/httpx"
)

const errNS = "https://api.rize-clone.example/errors/"

// Handler wires Service's business logic to chi HTTP handlers.
type Handler struct {
	Service *Service
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

// List handles GET /v1/activities.
func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
		return
	}

	q := r.URL.Query()

	from, err := parseRequiredTime(q.Get("from"))
	if err != nil {
		writeValidationError(w, r, "from must be an RFC3339 timestamp")
		return
	}
	to, err := parseRequiredTime(q.Get("to"))
	if err != nil {
		writeValidationError(w, r, "to must be an RFC3339 timestamp")
		return
	}

	limit := 0
	if raw := q.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeValidationError(w, r, "limit must be a positive integer")
			return
		}
		limit = parsed
	}

	items, nextCursor, hasMore, err := h.Service.List(r.Context(), identity.UserID, ListParams{
		From:       from,
		To:         to,
		AppID:      q.Get("app_id"),
		CategoryID: q.Get("category_id"),
		ProjectID:  q.Get("project_id"),
		DeviceID:   q.Get("device_id"),
		Precision:  q.Get("precision"),
		Cursor:     q.Get("cursor"),
		Limit:      limit,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}

	dtos := make([]eventDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toDTO(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, listResponse{Data: dtos, NextCursor: nextCursor, HasMore: hasMore})
}

func parseRequiredTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, errors.New("missing")
	}
	return time.Parse(time.RFC3339, raw)
}

func writeValidationError(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", detail)
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrValidation):
		httpx.WriteError(w, r, http.StatusBadRequest, errNS+"validation-error", "Validation Error", err.Error())
	default:
		httpx.WriteError(w, r, http.StatusInternalServerError, errNS+"internal-error", "Internal Server Error", "An unexpected error occurred.")
	}
}
