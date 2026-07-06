package reports

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts the six GET /v1/reports/* routes from
// documentation/api-reference.md §Activities & reports onto r.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler) {
	r.Route("/reports", func(r chi.Router) {
		r.Use(authenticate)
		r.Get("/summary", h.Summary)
		r.Get("/daily", h.Daily)
		r.Get("/categories", h.Categories)
		r.Get("/apps", h.Apps)
		r.Get("/projects", h.Projects)
		r.Get("/timeline", h.Timeline)
	})
}
