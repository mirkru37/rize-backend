package middleware

import (
	"crypto/rsa"
	"net/http"
	"strings"

	"github.com/mirkru37/rize-backend/internal/auth"
	"github.com/mirkru37/rize-backend/internal/httpx"
)

// Authenticate returns middleware implementing
// documentation/architecture-backend.md §Middleware Stack step 6 ("Auth —
// verifies the JWT ... and attaches the authenticated user to the request
// context; rejects unauthenticated requests to protected routes").
//
// It expects an `Authorization: Bearer <access-token>` header per
// documentation/api-reference.md §Conventions, verifies the token's RS256
// signature against publicKey, and rejects the request with 401 if the
// header is missing, malformed, or the token is invalid/expired.
func Authenticate(publicKey *rsa.PublicKey) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(header, prefix) {
				writeUnauthenticated(w, r)
				return
			}
			tokenString := strings.TrimSpace(strings.TrimPrefix(header, prefix))
			if tokenString == "" {
				writeUnauthenticated(w, r)
				return
			}

			claims, err := auth.VerifyAccessToken(publicKey, tokenString)
			if err != nil {
				writeUnauthenticated(w, r)
				return
			}

			identity := auth.Identity{
				UserID:   claims.Subject,
				Role:     claims.Role,
				DeviceID: claims.DeviceID,
			}
			ctx := auth.WithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireRole returns middleware implementing
// documentation/architecture-backend.md §Middleware Stack step 7 ("RBAC —
// authorizes the now-known, authenticated user against the requested
// route/resource"), gating routes (e.g. /v1/admin/*) on the authenticated
// identity's role claim per documentation/security.md §Role-based access
// control (RBAC). It must be mounted after Authenticate, which is what
// populates the identity this middleware reads.
func RequireRole(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity, ok := auth.IdentityFromContext(r.Context())
			if !ok {
				writeUnauthenticated(w, r)
				return
			}
			if identity.Role != role {
				httpx.WriteError(w, r, http.StatusForbidden,
					"https://api.rize-clone.example/errors/forbidden",
					"Forbidden",
					"You do not have permission to access this resource.",
				)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func writeUnauthenticated(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, http.StatusUnauthorized,
		"https://api.rize-clone.example/errors/unauthenticated",
		"Unauthenticated",
		"A valid access token is required.",
	)
}
