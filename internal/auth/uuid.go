package auth

import (
	"crypto/rand"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
)

// newUUIDv4 generates a random (version 4, RFC 4122) UUID for values the
// application must mint itself rather than delegating to Postgres'
// gen_random_uuid() — specifically, refresh_tokens.family_id, which sqlc's
// generated CreateRefreshToken query requires as an explicit parameter
// rather than a server-side default (see documentation/database-schema.md's
// refresh_tokens table).
func newUUIDv4() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("auth: generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant

	id := pgtype.UUID{Bytes: b, Valid: true}
	return id, nil
}

// parseUUID parses a canonical UUID string (e.g. from a URL path parameter
// or a client-supplied JSON field) into a pgtype.UUID.
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
