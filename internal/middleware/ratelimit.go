package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"github.com/mirkru37/rize-backend/internal/httpx"
)

// RateLimit returns middleware that throttles request volume per client IP
// before any auth or business logic runs, per
// documentation/architecture-backend.md §Middleware Stack step 5
// ("Rate limit ... throttles request volume before any auth or business
// logic runs, to protect the service from abusive or runaway clients").
//
// RIZ-30 assumption: documentation/architecture-backend.md explicitly
// flags the rate limiter's scope and thresholds as an open question, and
// documentation/security.md §API hardening only gives *suggested*,
// route-scoped figures (10/min per-IP on auth routes, 60/min per-user on
// sync/reports routes) that are marked "not final" and depend on
// authenticated-user context (per-user limiting) this ticket does not
// have, since there is no auth middleware yet. Rather than guess at
// per-route thresholds, RIZ-30 applies a single, simpler stack-wide
// per-IP token-bucket limit (requestsPerMinute, default 100 — see
// internal/config.DefaultRateLimitRequestsPerMinute) as a placeholder.
// Once auth exists, a future ticket should replace or augment this with
// the per-route, per-user limits security.md describes.
//
// The client IP is resolved via chi's ClientIPFromRemoteAddr — i.e. the
// literal TCP peer address (r.RemoteAddr) — rather than any
// client-supplied header (X-Forwarded-For, X-Real-IP, etc.), since this
// service does not sit behind a trusted reverse proxy that sets/strips
// those headers. Keying on a client-supplied header would let any client
// spoof its apparent IP, bypassing the per-IP budget and growing the
// limiter's per-key state without bound. If a reverse proxy/load balancer
// is introduced, this must be swapped for the appropriate
// middleware.ClientIPFromXFF/ClientIPFromHeader variant so the rate
// limiter keys off the real client IP instead of the proxy's address.
// Once the limit is hit, the response body is the same RFC 7807-style
// Problem envelope used everywhere else (per
// documentation/api-reference.md §Conventions), not httprate's default
// plain-text body; the Retry-After header httprate sets before invoking
// this handler is preserved.
func RateLimit(requestsPerMinute int) func(http.Handler) http.Handler {
	limiter := httprate.LimitBy(
		requestsPerMinute,
		time.Minute,
		func(r *http.Request) (string, error) {
			return httprate.CanonicalizeIP(middleware.GetClientIP(r.Context())), nil
		},
		httprate.WithLimitHandler(rateLimitedHandler),
	)

	return func(next http.Handler) http.Handler {
		return middleware.ClientIPFromRemoteAddr(limiter(next))
	}
}

// rateLimitedHandler writes the standard RFC 7807-style Problem body for a
// rate-limited request.
func rateLimitedHandler(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusTooManyRequests,
		"https://api.rize-clone.example/errors/rate-limit-exceeded",
		"Too Many Requests",
		"Request rate limit exceeded. Retry after the window indicated by the Retry-After header.",
	)
}
