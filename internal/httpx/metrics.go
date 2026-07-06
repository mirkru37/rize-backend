package httpx

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Standard HTTP metrics exposed at /metrics, per
// documentation/api-reference.md §Ops and
// documentation/architecture-backend.md §Config & Observability
// ("standard HTTP metrics (request counts, latencies, status codes)").
// RIZ-30 scope note: only these standard, protocol-level metrics are
// added here; service-specific counters (ingestion batch sizes, aggregate
// query latency, background job outcomes) belong to the tickets that
// implement those services, not this HTTP-server-foundation ticket.
var (
	httpRequestsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed, labeled by method, route pattern, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	httpRequestDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, labeled by method, route pattern, and status code.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)
)

// Metrics returns middleware that records standard HTTP metrics (request
// counts and latencies by method, route pattern, and status code) for
// every request, per documentation/api-reference.md §Ops.
func Metrics() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			duration := time.Since(start).Seconds()
			status := strconv.Itoa(ww.Status())
			path := routePattern(r)

			httpRequestsTotal.WithLabelValues(r.Method, path, status).Inc()
			httpRequestDuration.WithLabelValues(r.Method, path, status).Observe(duration)
		})
	}
}

// unmatchedRouteLabel is the "path" label value used for requests that did
// not match any registered chi route (e.g. 404s on arbitrary/junk paths).
// Labeling with the raw, client-controlled r.URL.Path instead would let a
// client generate unbounded Prometheus label cardinality — a memory-growth
// vector — simply by requesting distinct nonexistent paths. All unmatched
// requests are collapsed onto this single label value instead.
const unmatchedRouteLabel = "unmatched"

// routePattern returns the matched chi route pattern (e.g. "/v1/users/{id}")
// rather than the raw URL path, so metrics cardinality stays bounded
// regardless of path parameters. Falls back to the constant
// unmatchedRouteLabel — never the raw, client-controlled path — if no chi
// route context is available (e.g. for unmatched/404 requests).
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		if pattern := rctx.RoutePattern(); pattern != "" {
			return pattern
		}
	}
	return unmatchedRouteLabel
}
