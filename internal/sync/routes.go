package sync

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts the sync push and pull routes from
// documentation/api-reference.md §Sync onto r.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler) {
	r.Route("/sync", func(r chi.Router) {
		r.Use(authenticate)
		r.Post("/events", h.PushEvents)
		r.Get("/changes", h.PullChanges)
	})
}
