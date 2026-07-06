package reports

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// cursor is GET /v1/reports/timeline's keyset-pagination key: (started_at,
// event_id) — same shape and rationale as internal/activities' cursor,
// duplicated here rather than imported since the two packages are
// independently owned route groups (matching this repo's existing
// convention of small per-package uuid.go/cursor.go helpers, e.g. each
// CRUD group's own uuid.go).
type cursor struct {
	StartedAt time.Time
	EventID   string
}

// zeroCursor sorts before every real row; see internal/activities/cursor.go's
// zeroCursor doc comment for the rationale.
var zeroCursor = cursor{StartedAt: time.Unix(0, 0).UTC(), EventID: "00000000-0000-0000-0000-000000000000"}

func encodeCursor(c cursor) string {
	raw := fmt.Sprintf("%d:%s", c.StartedAt.UnixNano(), c.EventID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(raw string) (cursor, error) {
	if raw == "" {
		return zeroCursor, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return cursor{}, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return cursor{}, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	nanos, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return cursor{}, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	if _, err := parseUUID(parts[1]); err != nil {
		return cursor{}, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	return cursor{StartedAt: time.Unix(0, nanos).UTC(), EventID: parts[1]}, nil
}

func (c cursor) timestamptz() pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: c.StartedAt, Valid: true}
}

func (c cursor) uuid() pgtype.UUID {
	id, _ := parseUUID(c.EventID)
	return id
}
