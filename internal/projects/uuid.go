package projects

import (
	"crypto/rand"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// nowTimestamptz returns the current server time as a valid
// pgtype.Timestamptz, used by Service.Update to set/clear archived_at.
func nowTimestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true}
}

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

// newUUIDv7-equivalent: projects are a client-supplied-UUIDv7 entity per
// documentation/database-schema.md's PK convention, whether created via
// sync push or this direct REST endpoint. When a POST /v1/projects caller
// omits "id", the server falls back to a random (version 4) UUID rather
// than guessing at a UUIDv7 implementation — the client is expected to
// supply its own client-generated UUIDv7 to get the idempotency benefits
// documentation/sync-protocol.md describes; this fallback only exists so
// the endpoint still works end-to-end for a caller that omits it.
func newUUIDv4() (pgtype.UUID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return pgtype.UUID{}, fmt.Errorf("projects: generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return pgtype.UUID{Bytes: b, Valid: true}, nil
}
