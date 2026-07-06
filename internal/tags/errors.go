package tags

import "errors"

// Sentinel errors returned by Service methods; see internal/projects's
// errors.go doc comments — the same rationale applies here.
var (
	ErrValidation = errors.New("tags: validation error")
	ErrNotFound   = errors.New("tags: not found")
	// ErrConflict is returned for tags' UNIQUE (user_id, name) violation,
	// per documentation/database-schema.md ("prevents a user from creating
	// two tags with the same name").
	ErrConflict = errors.New("tags: conflict")
)
