package focussessions

import "errors"

var (
	// ErrValidation is returned for a malformed/invalid request.
	ErrValidation = errors.New("focussessions: validation error")
	// ErrNotFound is returned when the requested focus session does not
	// exist for the authenticated user, including when it belongs to a
	// different user (indistinguishable, per
	// documentation/security.md §Tenant Isolation).
	ErrNotFound = errors.New("focussessions: not found")
)
