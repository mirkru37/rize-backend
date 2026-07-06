package projects

import "errors"

// Sentinel errors returned by Service methods for a whole-request outcome
// that the handler maps to an RFC 7807-style Problem response.
var (
	// ErrValidation is returned for a malformed/invalid request (bad JSON,
	// blank required field, invalid id/cursor).
	ErrValidation = errors.New("projects: validation error")

	// ErrNotFound is returned when the requested project does not exist for
	// the authenticated user — including when the id exists but belongs to
	// a different user, per documentation/security.md §Tenant Isolation
	// ("indistinguishable from not existing at all").
	ErrNotFound = errors.New("projects: not found")

	// ErrConflict is returned for a unique-constraint violation (e.g. a
	// duplicate id).
	ErrConflict = errors.New("projects: conflict")
)
