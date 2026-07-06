// Package middleware provides the shared HTTP middleware stack wired in
// cmd/api and applied to every request in the fixed order mandated by
// documentation/architecture-backend.md §Middleware Stack:
//
//	RequestID -> Logging -> Recoverer -> CORS -> RateLimit -> Auth -> RBAC
//
// This package implements RequestID, Logging, Recoverer, CORS, and
// RateLimit. Auth (JWT verification) and RBAC (role authorization) are not
// implemented here — they depend on the auth/JWT work tracked separately —
// but the stack this package builds leaves an obvious attachment point
// for them between RateLimit and the handler, per RIZ-30.
package middleware
