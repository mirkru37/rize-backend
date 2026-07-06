package auth

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes mounts the auth, users, and devices route groups from
// documentation/api-reference.md onto r, per
// documentation/architecture-backend.md §Middleware Stack (Auth/RBAC run
// before any of the "authenticated" routes below; authenticate is the
// Auth middleware built by internal/middleware.Authenticate, injected here
// rather than imported directly so this package does not depend on
// internal/middleware).
//
// Routes explicitly out of MVP scope for RIZ-32 (Sign in with Apple,
// password reset, GDPR delete/export, admin) are wired as 501 Not
// Implemented stubs so the full route surface from api-reference.md exists
// and returns the standard Problem envelope rather than a generic 404.
func RegisterRoutes(r chi.Router, h *Handler, authenticate func(http.Handler) http.Handler, requireAdmin func(http.Handler) http.Handler) {
	r.Route("/auth", func(r chi.Router) {
		r.Post("/register", h.Register)
		r.Post("/login", h.Login)
		r.Post("/refresh", h.Refresh)
		r.Post("/apple", notImplementedStub("Sign in with Apple is not implemented yet."))
		r.Post("/password/forgot", notImplementedStub("Password reset is not implemented yet."))
		r.Post("/password/reset", notImplementedStub("Password reset is not implemented yet."))

		r.Group(func(r chi.Router) {
			r.Use(authenticate)
			r.Post("/logout", h.Logout)
		})
	})

	r.Route("/users/me", func(r chi.Router) {
		r.Use(authenticate)
		r.Get("/", h.GetMe)
		r.Patch("/", h.PatchMe)
		r.Delete("/", notImplementedStub("Account deletion is not implemented yet."))
		r.Post("/export", notImplementedStub("Data export is not implemented yet."))
	})

	r.Route("/devices", func(r chi.Router) {
		r.Use(authenticate)
		r.Get("/", h.ListDevices)
		r.Patch("/{id}", h.PatchDevice)
		r.Delete("/{id}", h.DeleteDevice)
	})

	r.Route("/admin", func(r chi.Router) {
		r.Use(authenticate)
		r.Use(requireAdmin)
		r.Get("/users", notImplementedStub("Admin user listing is not implemented yet."))
		r.Patch("/users/{id}", notImplementedStub("Admin user update is not implemented yet."))
	})
}
