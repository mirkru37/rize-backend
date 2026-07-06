package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// RequestID assigns a unique ID to each incoming request, attaching it to
// the request context so downstream logging and error responses can be
// correlated, per documentation/architecture-backend.md §Middleware Stack
// step 1. It wraps chi's built-in RequestID middleware; consumers that
// need to read the ID back out of a request context (e.g. internal/httpx)
// should use chi's middleware.GetReqID directly.
func RequestID(next http.Handler) http.Handler {
	return middleware.RequestID(next)
}
