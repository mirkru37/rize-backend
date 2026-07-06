package store

import (
	"encoding/base64"
	"fmt"
	"math"
	"strconv"
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
