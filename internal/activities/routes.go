package activities

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts GET /v1/activities per
// documentation/api-reference.md §Activities & reports onto r.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler) {
	r.Route("/activities", func(r chi.Router) {
		r.Use(authenticate)
		r.Get("/", h.List)
	})
}
