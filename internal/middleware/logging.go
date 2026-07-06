package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
)

// Logging returns middleware that logs each request with structured
// (slog) key-value fields once the request has completed: method, path,
// status, byte count, latency, and request ID, per
// documentation/architecture-backend.md §Middleware Stack step 2 and
// §Config & Observability ("structured ... rather than free-text").
//
// Per documentation/security.md's "no PII in logs" requirement, only the
// request path and method are logged, never query strings or bodies that
// might carry user-identifying data.
func Logging(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, req.ProtoMajor)

			next.ServeHTTP(ww, req)

			logger.Info("request",
				"method", req.Method,
				"path", req.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(req.Context()),
			)
		})
	}
}
