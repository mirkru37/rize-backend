package activities

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// cursor is GET /v1/activities' keyset-pagination key: (started_at,
// event_id), matching the ORDER BY in
// internal/store/queries/activities.sql's ListActivityEventsForUser. A
// plain server_seq cursor (like the CRUD groups' list endpoints use) isn't
// a good fit here: activities are naturally consumed in chronological
// order over an explicit caller-supplied time range, and started_at is the
// hypertable's partitioning column, so keying on it lets Postgres use the
// activity_events_user_started_idx index.
type cursor struct {
	StartedAt time.Time
	EventID   string
}

// zeroCursor sorts before every real row: the Unix epoch predates any
// tracked activity (activity_events didn't exist before this system did),
// and the nil UUID sorts before every real (non-nil) UUIDv7. Unlike
// Postgres' timestamptz "-infinity", the Unix epoch round-trips cleanly
// through time.Time/pgtype.Timestamptz.
var zeroCursor = cursor{StartedAt: time.Unix(0, 0).UTC(), EventID: "00000000-0000-0000-0000-000000000000"}

// encodeCursor turns a cursor into an opaque token, per
// documentation/api-reference.md §Conventions ("cursor is an opaque token
// ... must not be parsed or constructed by clients"), following the same
// base64(<fields>) convention as internal/store/cursor.go.
func encodeCursor(c cursor) string {
	raw := fmt.Sprintf("%d:%s", c.StartedAt.UnixNano(), c.EventID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor. An empty string decodes to
// zeroCursor (the beginning of the range), per
// documentation/sync-protocol.md §Pull's "omitted or empty starts from the
// beginning" convention, reused here for consistency.
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
