package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// Recoverer recovers from panics in downstream handlers, converting them
// into a 500 response instead of crashing the process, per
// documentation/architecture-backend.md §Middleware Stack step 3. It wraps
// chi's built-in Recoverer middleware.
func Recoverer(next http.Handler) http.Handler {
	return middleware.Recoverer(next)
}
