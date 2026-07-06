package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
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
// literal TCP peer address — mirroring the same "no untrusted proxy in
// front of the service yet" assumption already made for the RealIP
// middleware in this package. If a reverse proxy/load balancer is
// introduced, this must be swapped for the appropriate
// middleware.ClientIPFromXFF/ClientIPFromHeader variant so the rate
// limiter keys off the real client IP instead of the proxy's address.
func RateLimit(requestsPerMinute int) func(http.Handler) http.Handler {
	limiter := httprate.LimitBy(requestsPerMinute, time.Minute, func(r *http.Request) (string, error) {
		return httprate.CanonicalizeIP(middleware.GetClientIP(r.Context())), nil
	})

	return func(next http.Handler) http.Handler {
		return middleware.ClientIPFromRemoteAddr(limiter(next))
	}
}
