package store

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// ConstraintViolation inspects err for a Postgres constraint-violation
// SQLSTATE and, if found, returns a (code, message) pair suitable for a
// per-item/per-request RFC 7807-style 4xx response instead of a
// batch/request-aborting 500. ok is false for any other error (including
// no error at all), signaling the caller should treat it as an unexpected
// failure instead.
//
// This is the same pattern internal/sync/service.go's
// constraintViolationResult established for RIZ-33's push endpoint,
// factored out here so the CRUD route groups added in RIZ-34 (projects,
// tags, categories, focus-sessions) can share it rather than re-implement
// it per package.
//
// Messages are static or built only from pgErr.ConstraintName — never from
// pgErr.Message or the offending payload values, which can echo submitted
// data back to the client.
func ConstraintViolation(err error) (code, message string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", "", false
	}
	switch pgErr.Code {
	case "23503": // foreign_key_violation
		return "FOREIGN_KEY_VIOLATION", "referenced row does not exist", true
	case "23505": // unique_violation
		message := "value conflicts with an existing record"
		if pgErr.ConstraintName != "" {
			message = fmt.Sprintf("value conflicts with an existing record (constraint %q)", pgErr.ConstraintName)
		}
		return "CONFLICT", message, true
	case "23514", "23502": // check/not_null violation
		message := "value violates a database constraint"
		if pgErr.ConstraintName != "" {
			message = fmt.Sprintf("value violates constraint %q", pgErr.ConstraintName)
		}
		return "VALIDATION_ERROR", message, true
	default:
		return "", "", false
	}
}
