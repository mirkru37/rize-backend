// Package middleware will provide the shared HTTP middleware stack wired in
// cmd/api and applied to every request in a fixed order: request ID,
// structured per-request logging, panic recovery, CORS, rate limiting
// (per-IP for auth routes, per-user for sync/reports routes), JWT
// authentication, and RBAC authorization. Request ID, logging, and recovery
// are already wired in cmd/api using Chi's built-in middleware as scaffolding
// stand-ins; this package will grow the CORS, rate-limit, auth, and RBAC
// middleware described in documentation/architecture-backend.md and
// documentation/security.md.
package middleware
