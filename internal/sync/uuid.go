package sync

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// parseUUID parses a canonical UUID string (e.g. a client-supplied
// event_id/id or the request's device_id) into a pgtype.UUID.
func parseUUID(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: invalid id %q", ErrValidation, s)
	}
	if !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("%w: invalid id %q", ErrValidation, s)
	}
	return id, nil
}

// parseOptionalUUID parses s into a pgtype.UUID, returning a zero-value,
// invalid pgtype.UUID (i.e. SQL NULL) when s is empty.
func parseOptionalUUID(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(s)
}
