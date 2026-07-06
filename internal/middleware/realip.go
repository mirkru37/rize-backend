package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5/middleware"
)

// RealIP sets a request's RemoteAddr from the X-Forwarded-For or
// X-Real-IP headers, if present, so downstream middleware and handlers see
// the client's real IP rather than the immediate peer's. It wraps chi's
// built-in RealIP middleware and is kept immediately after RequestID (as
// in the original scaffolding), since the rate-limit middleware and
// per-request logging both depend on an accurate client IP.
//
//nolint:staticcheck // RealIP is required by the mandated middleware order; the service does not sit behind an untrusted proxy at this scaffolding stage.
func RealIP(next http.Handler) http.Handler {
	return middleware.RealIP(next)
}
