package middleware

import (
	"net/http"

	"github.com/go-chi/cors"
)

// CORSConfig configures the CORS middleware. AllowedOrigins should list
// the explicit origins permitted to make cross-origin requests, per
// documentation/security.md §API hardening ("CORS is locked to known
// origins ... there is no wildcard (*) CORS origin in any environment").
//
// RIZ-30 assumption: for local development convenience, this ticket's
// default (see internal/config.DefaultCORSAllowedOrigins) allows every
// origin ("*"). This is documented here as a deliberate scaffolding
// assumption, not a claim that it satisfies the "no wildcard in any
// environment" requirement above — any deployed environment must set
// CORS_ALLOWED_ORIGINS to an explicit origin list before going live.
type CORSConfig struct {
	AllowedOrigins []string
}

// CORS returns middleware that applies the given cross-origin policy, per
// documentation/architecture-backend.md §Middleware Stack step 4.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	return cors.Handler(cors.Options{
		AllowedOrigins:   cfg.AllowedOrigins,
		AllowedMethods:   []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		ExposedHeaders:   []string{"X-Request-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	})
}
