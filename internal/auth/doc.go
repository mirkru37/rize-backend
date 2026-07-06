// Package auth will implement authentication and authorization for
// rize-backend: email/password registration and login, Sign in with Apple
// identity-token exchange, issuance and verification of short-lived JWT
// access tokens, rotation of opaque refresh tokens, and role-based access
// control (RBAC) for authenticated and admin-only routes. It sits behind the
// HTTP handlers under the auth and users route groups and is the only
// package responsible for producing an authenticated user identity that
// other services (sync, reports) can rely on.
package auth
