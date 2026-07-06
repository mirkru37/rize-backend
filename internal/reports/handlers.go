package reports

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/httpx"
)

const errNS = "https://api.rize-clone.example/errors/"

// Handler wires Service's business logic to chi HTTP handlers for the six
// GET /v1/reports/* routes.
type Handler struct {
	Service *Service
}

// NewHandler returns a Handler backed by service.
func NewHandler(service *Service) *Handler {
	return &Handler{Service: service}
}

func filtersFromQuery(q url.Values) reportFilters {
	return reportFilters{
		AppID:      q.Get("app_id"),
		CategoryID: q.Get("category_id"),
		ProjectID:  q.Get("project_id"),
		DeviceID:   q.Get("device_id"),
		Precision:  q.Get("precision"),
	}
}

func parseFromTo(q url.Values) (time.Time, time.Time, error) {
	fromRaw, toRaw := q.Get("from"), q.Get("to")
	if fromRaw == "" || toRaw == "" {
		return time.Time{}, time.Time{}, errors.New("from and to are required")
	}
	from, err := time.Parse(time.RFC3339, fromRaw)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("from must be an RFC3339 timestamp")
	}
	to, err := time.Parse(time.RFC3339, toRaw)
	if err != nil {
		return time.Time{}, time.Time{}, errors.New("to must be an RFC3339 timestamp")
	}
	return from, to, nil
}

// Summary handles GET /v1/reports/summary.
func (h *Handler) Summary(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	from, to, err := parseFromTo(r.URL.Query())
	if err != nil {
		writeValidationError(w, r, err.Error())
		return
	}
	result, err := h.Service.Summary(r.Context(), identity.UserID, from, to, filtersFromQuery(r.URL.Query()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, summaryDTO{
		From:                result.From.UTC().Format(timeLayout),
		To:                  result.To.UTC().Format(timeLayout),
		TotalTrackedSeconds: result.TotalTrackedSeconds,
		Categories:          toCategoryBreakdown(result.Categories),
	})
}

// Daily handles GET /v1/reports/daily?date=YYYY-MM-DD.
func (h *Handler) Daily(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	dateRaw := r.URL.Query().Get("date")
	date, err := time.Parse(dateLayout, dateRaw)
	if err != nil {
		writeValidationError(w, r, "date must be formatted as YYYY-MM-DD")
		return
	}
	result, err := h.Service.Daily(r.Context(), identity.UserID, date, filtersFromQuery(r.URL.Query()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, dailyDTO{
		Date:                result.Date.Format(dateLayout),
		TotalTrackedSeconds: result.TotalTrackedSeconds,
		Categories:          toCategoryBreakdown(result.Categories),
	})
}

// Categories handles GET /v1/reports/categories.
func (h *Handler) Categories(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	from, to, err := parseFromTo(r.URL.Query())
	if err != nil {
		writeValidationError(w, r, err.Error())
		return
	}
	result, err := h.Service.Categories(r.Context(), identity.UserID, from, to, filtersFromQuery(r.URL.Query()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, categoriesDTO{
		From:       result.From.UTC().Format(timeLayout),
		To:         result.To.UTC().Format(timeLayout),
		Categories: toCategoryBreakdown(result.Categories),
	})
}

// Apps handles GET /v1/reports/apps.
func (h *Handler) Apps(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	from, to, err := parseFromTo(r.URL.Query())
	if err != nil {
		writeValidationError(w, r, err.Error())
		return
	}
	result, err := h.Service.Apps(r.Context(), identity.UserID, from, to, filtersFromQuery(r.URL.Query()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, appsDTO{
		From: result.From.UTC().Format(timeLayout),
		To:   result.To.UTC().Format(timeLayout),
		Apps: toAppBreakdown(result.Apps),
	})
}

// Projects handles GET /v1/reports/projects.
func (h *Handler) Projects(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	from, to, err := parseFromTo(r.URL.Query())
	if err != nil {
		writeValidationError(w, r, err.Error())
		return
	}
	result, err := h.Service.Projects(r.Context(), identity.UserID, from, to, filtersFromQuery(r.URL.Query()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusOK, projectsDTO{
		From:     result.From.UTC().Format(timeLayout),
		To:       result.To.UTC().Format(timeLayout),
		Projects: toProjectBreakdown(result.Projects),
	})
}

// Timeline handles GET /v1/reports/timeline.
func (h *Handler) Timeline(w http.ResponseWriter, r *http.Request) {
	identity, ok := auth.IdentityFromContext(r.Context())
	if !ok {
		writeUnauthenticated(w, r)
		return
	}
	q := r.URL.Query()
	from, to, err := parseFromTo(q)
	if err != nil {
		writeValidationError(w, r, err.Error())
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
	items, nextCursor, hasMore, err := h.Service.Timeline(r.Context(), identity.UserID, from, to, filtersFromQuery(q), q.Get("cursor"), limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	dtos := make([]timelineEventDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toTimelineEventDTO(item))
	}
	httpx.WriteJSON(w, r, http.StatusOK, timelineResponse{Data: dtos, NextCursor: nextCursor, HasMore: hasMore})
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusUnauthorized, errNS+"unauthenticated", "Unauthenticated", "A valid access token is required.")
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
