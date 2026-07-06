package reports

import "errors"

// ErrValidation is returned for a malformed/invalid request: a bad filter
// id, an invalid precision value, or an invalid/absurd time range.
var ErrValidation = errors.New("reports: validation error")

// errInvalidUUID is wrapped into ErrValidation by callers; it never
// escapes this package on its own.
var errInvalidUUID = errors.New("invalid uuid")
