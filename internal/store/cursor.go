package store

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// EncodeCursor turns a server_seq value into the opaque cursor token
// documentation/api-reference.md §Conventions and
// documentation/sync-protocol.md §Pull describe ("cursor is an opaque
// token ... must not be parsed or constructed by clients"). Base64-encoding
// the decimal server_seq is enough to make the value opaque to a
// conforming client while staying trivially decodable server-side; it is
// not a security boundary; a client only ever needs to store and echo it
// back verbatim.
func EncodeCursor(serverSeq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(serverSeq, 10)))
}

// DecodeCursor reverses EncodeCursor. An empty string decodes to 0 (the
// beginning of the change stream), per documentation/sync-protocol.md §Pull
// ("On a client's first-ever pull, cursor is omitted or empty, and the
// server starts from the beginning of that user's change stream").
func DecodeCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, fmt.Errorf("store: decode cursor: %w", err)
	}
	seq, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("store: decode cursor: %w", err)
	}
	if seq < 0 {
		return 0, fmt.Errorf("store: decode cursor: negative server_seq")
	}
	return seq, nil
}

// PullCursor is the opaque cursor's decoded shape used by GET
// /v1/sync/changes (internal/sync's pull service) only. Unlike the plain
// server_seq cursor above (used by the CRUD groups' list endpoints, whose
// pagination is unaffected by this ticket's H1 fix), the sync pull's
// keyset-pagination key is the tuple (xid8-widened row xmin, server_seq)
// — see migration 000025's comment for why server_seq alone is unsafe as a
// pagination key and why anchoring to the same xid8 value the horizon gate
// already uses closes that gap. Xid8 is the low 64 bits of the row's
// widened transaction id (migration 000025's xmin_xid8 SQL function);
// ServerSeq is the same per-row change counter the wire DTOs already echo.
// The zero value (Xid8: 0, ServerSeq: 0) means "from the beginning of the
// change stream": every real row's widened xid8 is > 0 (Postgres never
// assigns transaction id 0), so (0, 0) sorts before every real row's tuple.
type PullCursor struct {
	Xid8      uint64
	ServerSeq int64
}

// EncodePullCursor turns a PullCursor into the opaque cursor token
// documentation/api-reference.md §Conventions and
// documentation/sync-protocol.md §Pull describe ("cursor is an opaque
// token ... must not be parsed or constructed by clients"). Encoding the
// wire format as base64("<xid8>:<server_seq>") keeps it opaque to a
// conforming client while staying trivially decodable server-side, exactly
// like EncodeCursor above; it is not a security boundary.
func EncodePullCursor(c PullCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%d:%d", c.Xid8, c.ServerSeq)))
}

// DecodePullCursor reverses EncodePullCursor. An empty string decodes to
// the zero PullCursor (the beginning of the change stream), per
// documentation/sync-protocol.md §Pull ("On a client's first-ever pull,
// cursor is omitted or empty, and the server starts from the beginning of
// that user's change stream").
func DecodePullCursor(cursor string) (PullCursor, error) {
	if cursor == "" {
		return PullCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return PullCursor{}, fmt.Errorf("store: decode pull cursor: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return PullCursor{}, fmt.Errorf("store: decode pull cursor: malformed cursor")
	}
	xid8, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return PullCursor{}, fmt.Errorf("store: decode pull cursor: %w", err)
	}
	seq, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return PullCursor{}, fmt.Errorf("store: decode pull cursor: %w", err)
	}
	if seq < 0 {
		return PullCursor{}, fmt.Errorf("store: decode pull cursor: negative server_seq")
	}
	return PullCursor{Xid8: xid8, ServerSeq: seq}, nil
}

// LimitParam clamps n (a caller-computed page size, e.g. "requested limit
// + 1" used by every keyset-paginated list/pull query in this ticket to
// detect "more rows exist") to a value that safely fits in the int32
// sqlc generates for a SQL LIMIT parameter. Every caller of this function
// already bounds n well below MaxInt32 via its own defaultLimit/maxLimit
// constants; this clamp exists only to give the compiler (and gosec) a
// provably safe int-to-int32 conversion, per internal/auth/password.go's
// existing "bounds-checked" convention for this class of narrowing
// conversion, without scattering an inline //nolint comment per call
// site.
func LimitParam(n int) int32 {
	if n < 1 {
		return 1
	}
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(n)
}
