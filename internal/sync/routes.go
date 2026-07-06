package sync

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts the sync push route from
// documentation/api-reference.md §Sync onto r. GET /v1/sync/changes (pull)
// is out of scope for RIZ-33 and is intentionally not mounted here.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler) {
	r.Route("/sync", func(r chi.Router) {
		r.Use(authenticate)
		r.Post("/events", h.PushEvents)
	})
}
