package auth

import (
	"net/http"

	"github.com/mirkru37/rize-backend/internal/httpx"
)

// notImplementedStub returns a handler that responds 501 Not Implemented
// with the standard RFC 7807-style Problem envelope, for routes that are
// explicitly out of scope for this ticket (RIZ-32):
// POST /v1/auth/apple, POST /v1/auth/password/forgot,
// POST /v1/auth/password/reset, DELETE /v1/users/me,
// POST /v1/users/me/export, and the /v1/admin/* routes.
func notImplementedStub(detail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotImplemented,
			errNS+"not-implemented",
			"Not Implemented",
			detail,
		)
	}
}
