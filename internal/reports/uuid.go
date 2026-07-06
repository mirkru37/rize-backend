package reports

import "github.com/jackc/pgx/v5/pgtype"

// parseUUID parses a canonical UUID string into a pgtype.UUID.
func parseUUID(s string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(s); err != nil {
		return pgtype.UUID{}, errInvalidUUID
	}
	if !id.Valid {
		return pgtype.UUID{}, errInvalidUUID
	}
	return id, nil
}

// parseOptionalUUID parses s into a pgtype.UUID, returning a zero-value,
// invalid pgtype.UUID (SQL NULL, i.e. "filter not set") when s is empty.
func parseOptionalUUID(s string) (pgtype.UUID, error) {
	if s == "" {
		return pgtype.UUID{}, nil
	}
	return parseUUID(s)
}
