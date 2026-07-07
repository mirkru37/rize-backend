package middleware

import (
	"net/http"
	"runtime/debug"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/mirkru37/rize-backend/internal/observability"
)

// Recoverer recovers from panics in downstream handlers, converting them
// into a 500 response instead of crashing the process, per
// documentation/architecture-backend.md §Middleware Stack step 3. It
// reimplements chi's built-in middleware.Recoverer (rather than wrapping
// it) so a recovered panic can also be reported to Sentry (RIZ-53 /
// documentation/observability.md: "Panics are captured alongside the
// existing recoverer middleware — Sentry does not replace panic recovery,
// it observes it") before the existing logging-and-500 behavior runs
// completely unchanged.
func Recoverer(next http.Handler) http.Handler {
	fn := func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				if rvr == http.ErrAbortHandler { //nolint:errorlint // matches chi's own recover() comparison
					// we don't recover http.ErrAbortHandler so the response
					// to the client is aborted, this should not be logged
					// (or reported to Sentry).
					panic(rvr)
				}

				observability.CapturePanic(r.Context(), rvr)

				logEntry := middleware.GetLogEntry(r)
				if logEntry != nil {
					logEntry.Panic(rvr, debug.Stack())
				} else {
					middleware.PrintPrettyStack(rvr)
				}

				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
		}()

		next.ServeHTTP(w, r)
	}

	return http.HandlerFunc(fn)
}
