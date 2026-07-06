package categories

import "errors"

var (
	// ErrValidation is returned for a malformed/invalid request.
	ErrValidation = errors.New("categories: validation error")
	// ErrNotFound covers "doesn't exist," "belongs to another user," and
	// "is a system default and this operation only applies to categories
	// you own" — see doc.go for why all three collapse to the same 404.
	ErrNotFound = errors.New("categories: not found")
)
