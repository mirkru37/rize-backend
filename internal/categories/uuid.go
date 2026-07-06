package categories

import (
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// parseUUID parses a canonical UUID string into a pgtype.UUID.
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
