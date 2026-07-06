package auth

import "context"

// contextKey is an unexported type for context keys defined in this package,
// per the standard library's convention for avoiding collisions between
// packages that both use context.WithValue.
type contextKey int

const identityContextKey contextKey = iota

// Identity is the authenticated caller's identity, extracted from a verified
// access token's claims and attached to the request context by the auth
// middleware (see internal/middleware.Authenticate), per
// documentation/architecture-backend.md §Middleware Stack step 6 ("Auth ...
// attaches the authenticated user to the request context").
type Identity struct {
	UserID   string
	Role     string
	DeviceID string
}

// WithIdentity returns a new context with identity attached.
func WithIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityContextKey, identity)
}

// IdentityFromContext returns the authenticated identity attached to ctx by
// the auth middleware, and whether one was present.
func IdentityFromContext(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityContextKey).(Identity)
	return identity, ok
}
