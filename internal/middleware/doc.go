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
//
// RIZ-30 fix note: chi's RealIP middleware (which trusts the
// X-Forwarded-For/X-Real-IP request headers) was deliberately removed from
// this stack. The service does not sit behind a trusted reverse proxy that
// strips/sets those headers, so honoring them lets any client spoof its
// apparent IP, defeating the per-IP rate limiter and allowing unbounded
// per-IP state growth. The rate limiter keys on the literal TCP peer
// address (r.RemoteAddr) instead; see RateLimit below.
package middleware
