package tags

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts the /v1/tags CRUD route group from
// documentation/api-reference.md §CRUD groups onto r.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler) {
	r.Route("/tags", func(r chi.Router) {
		r.Use(authenticate)
		r.Get("/", h.List)
		r.Post("/", h.Create)
		r.Get("/{id}", h.Get)
		r.Patch("/{id}", h.Patch)
		r.Delete("/{id}", h.Delete)
	})
}
