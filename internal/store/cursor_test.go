package store_test

import (
	"testing"

	"github.com/mirkru37/rize-backend/internal/store"
)

func TestEncodeDecodeCursor(t *testing.T) {
	tests := []struct {
		name      string
		serverSeq int64
	}{
		{name: "zero", serverSeq: 0},
		{name: "positive", serverSeq: 42},
		{name: "large", serverSeq: 9_223_372_036_854_775_807},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			encoded := store.EncodeCursor(tt.serverSeq)
			got, err := store.DecodeCursor(encoded)
			if err != nil {
				t.Fatalf("DecodeCursor(%q): %v", encoded, err)
			}
			if got != tt.serverSeq {
				t.Errorf("round-trip = %d, want %d", got, tt.serverSeq)
			}
		})
	}
}

func TestDecodeCursorEmptyStringIsBeginning(t *testing.T) {
	got, err := store.DecodeCursor("")
	if err != nil {
		t.Fatalf("DecodeCursor(\"\"): %v", err)
	}
	if got != 0 {
		t.Errorf("DecodeCursor(\"\") = %d, want 0", got)
	}
}

func TestDecodeCursorRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!not-base64!!!"},
		{name: "base64 but not an integer", cursor: "bm90LWFuLWludA"}, // "not-an-int"
		{name: "negative server_seq", cursor: store.EncodeCursor(-1)},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if _, err := store.DecodeCursor(tt.cursor); err == nil {
				t.Errorf("DecodeCursor(%q) = nil error, want an error", tt.cursor)
			}
		})
	}
}

func TestEncodeDecodePullCursor(t *testing.T) {
	tests := []struct {
		name string
		c    store.PullCursor
	}{
		{name: "zero value", c: store.PullCursor{}},
		{name: "typical", c: store.PullCursor{Xid8: 123456, ServerSeq: 42}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			encoded := store.EncodePullCursor(tt.c)
			got, err := store.DecodePullCursor(encoded)
			if err != nil {
				t.Fatalf("DecodePullCursor(%q): %v", encoded, err)
			}
			if got != tt.c {
				t.Errorf("round-trip = %+v, want %+v", got, tt.c)
			}
		})
	}
}

func TestDecodePullCursorEmptyStringIsZeroValue(t *testing.T) {
	got, err := store.DecodePullCursor("")
	if err != nil {
		t.Fatalf("DecodePullCursor(\"\"): %v", err)
	}
	if got != (store.PullCursor{}) {
		t.Errorf("DecodePullCursor(\"\") = %+v, want zero value", got)
	}
}

func TestDecodePullCursorRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!!not-base64!!!"},
		{name: "no colon separator", cursor: "bm8tY29sb24taGVyZQ"}, // "no-colon-here"
		{name: "non-numeric xid8", cursor: "eHl6OjE"},              // "xyz:1"
		{name: "non-numeric server_seq", cursor: "MTp4eXo"},        // "1:xyz"
		{name: "negative server_seq", cursor: "MTotMQ"},            // "1:-1"
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if _, err := store.DecodePullCursor(tt.cursor); err == nil {
				t.Errorf("DecodePullCursor(%q) = nil error, want an error", tt.cursor)
			}
		})
	}
}

// TestPullCursorIsZero covers store.PullCursor.IsZero, which RIZ-72's pull
// cursor-reset check (internal/sync/pull.go) uses to exempt a first-ever
// pull (empty/zero cursor) from ever being compared against the retained
// prune horizon.
func TestPullCursorIsZero(t *testing.T) {
	tests := []struct {
		name string
		c    store.PullCursor
		want bool
	}{
		{name: "zero value", c: store.PullCursor{}, want: true},
		{name: "zero xid8, nonzero seq", c: store.PullCursor{Xid8: 0, ServerSeq: 1}, want: false},
		{name: "nonzero xid8, zero seq", c: store.PullCursor{Xid8: 1, ServerSeq: 0}, want: false},
		{name: "both nonzero", c: store.PullCursor{Xid8: 5, ServerSeq: 5}, want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.IsZero(); got != tt.want {
				t.Errorf("PullCursor(%+v).IsZero() = %v, want %v", tt.c, got, tt.want)
			}
		})
	}
}

// TestPullCursorLess covers store.PullCursor.Less, the (xid8, server_seq)
// keyset ordering RIZ-72's cursor-reset check compares a caller's cursor
// against the persisted horizon with.
func TestPullCursorLess(t *testing.T) {
	tests := []struct {
		name string
		a, b store.PullCursor
		want bool
	}{
		{name: "equal tuples", a: store.PullCursor{Xid8: 10, ServerSeq: 20}, b: store.PullCursor{Xid8: 10, ServerSeq: 20}, want: false},
		{name: "lower xid8 wins regardless of server_seq", a: store.PullCursor{Xid8: 1, ServerSeq: 999}, b: store.PullCursor{Xid8: 2, ServerSeq: 0}, want: true},
		{name: "higher xid8 loses regardless of server_seq", a: store.PullCursor{Xid8: 2, ServerSeq: 0}, b: store.PullCursor{Xid8: 1, ServerSeq: 999}, want: false},
		{name: "same xid8, lower server_seq wins", a: store.PullCursor{Xid8: 5, ServerSeq: 1}, b: store.PullCursor{Xid8: 5, ServerSeq: 2}, want: true},
		{name: "same xid8, higher server_seq loses", a: store.PullCursor{Xid8: 5, ServerSeq: 2}, b: store.PullCursor{Xid8: 5, ServerSeq: 1}, want: false},
		{name: "zero cursor is less than any real cursor", a: store.PullCursor{}, b: store.PullCursor{Xid8: 1, ServerSeq: 0}, want: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.a.Less(tt.b); got != tt.want {
				t.Errorf("PullCursor(%+v).Less(%+v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestLimitParam(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int32
	}{
		{name: "typical", n: 50, want: 50},
		{name: "zero clamps to 1", n: 0, want: 1},
		{name: "negative clamps to 1", n: -5, want: 1},
		{name: "over int32 max clamps to int32 max", n: 1 << 40, want: 1<<31 - 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := store.LimitParam(tt.n); got != tt.want {
				t.Errorf("LimitParam(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}
