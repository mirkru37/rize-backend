package focussessions

import (
	"crypto/rand"
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

// parseOptionalUUID parses s into a pgtype.UUID, returning a zero-value,
// invalid pgtype.UUID (SQL NULL) when s is empty.
func parseOptionalUUID(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(s)
}

// newUUIDv4 is the fallback used when POST /v1/focus-sessions omits "id";
// see internal/projects/uuid.go's newUUIDv4 doc comment for the rationale.
func newUUIDv4() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("focussessions: generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}
