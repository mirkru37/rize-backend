package activities

import "errors"

var (
	// ErrValidation is returned for a malformed/invalid request: a bad
	// filter id, an invalid precision value, or an invalid/absurd time
	// range (see service.go's validateRange).
	ErrValidation = errors.New("activities: validation error")

	// errInvalidUUID is wrapped into ErrValidation by callers; it never
	// escapes this package on its own.
	errInvalidUUID = errors.New("invalid uuid")
)
