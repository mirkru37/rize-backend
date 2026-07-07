package reports

import (
	"testing"
	"time"
)

func TestMinTime(t *testing.T) {
	earlier := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	later := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		a, b time.Time
		want time.Time
	}{
		{name: "a before b", a: earlier, b: later, want: earlier},
		{name: "b before a", a: later, b: earlier, want: earlier},
		{name: "equal", a: earlier, b: earlier, want: earlier},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := minTime(tt.a, tt.b); !got.Equal(tt.want) {
				t.Errorf("minTime() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWindowSecondsClampsToAtLeastOne guards the LEAST(device_total_s,
// window_seconds) invariant in CategoryTotalsForRange/AppTotalsForRange
// (internal/store/queries/activities.sql): a zero or negative
// window_seconds would zero out every capped total, silently. windowSeconds
// is only ever called with a genuinely closed (from-before-to) window in
// production, but this asserts the defensive clamp holds even for a
// degenerate window rather than relying on that invariant never regressing.
func TestWindowSecondsClampsToAtLeastOne(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		w    window
		want int64
	}{
		{name: "one day", w: window{From: base, To: base.AddDate(0, 0, 1)}, want: 86400},
		{name: "one hour", w: window{From: base, To: base.Add(time.Hour)}, want: 3600},
		{name: "empty window clamps to 1", w: window{From: base, To: base}, want: 1},
		{name: "inverted window clamps to 1", w: window{From: base.AddDate(0, 0, 1), To: base}, want: 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := windowSeconds(tt.w); got != tt.want {
				t.Errorf("windowSeconds() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestDecodeCursorInternal(t *testing.T) {
	c := cursor{StartedAt: time.Date(2024, 1, 1, 9, 0, 0, 0, time.UTC), EventID: "123e4567-e89b-12d3-a456-426614174000"}
	encoded := encodeCursor(c)

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "empty is zero cursor", raw: "", wantErr: false},
		{name: "valid round-trip", raw: encoded, wantErr: false},
		{name: "not base64", raw: "!!!not-base64!!!", wantErr: true},
		{name: "no colon separator", raw: "bm8tY29sb24taGVyZQ", wantErr: true},
		{name: "non-numeric timestamp", raw: "eHl6OjEyM2U0NTY3LWU4OWItMTJkMy1hNDU2LTQyNjYxNDE3NDAwMA", wantErr: true},
		{name: "invalid event id", raw: "MTpub3QtYS11dWlk", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeCursor(tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("decodeCursor(%q) error = %v, wantErr %v", tt.raw, err, tt.wantErr)
			}
			if tt.raw == "" && got != zeroCursor {
				t.Errorf("decodeCursor(\"\") = %+v, want zeroCursor %+v", got, zeroCursor)
			}
			if tt.raw == encoded && (!got.StartedAt.Equal(c.StartedAt) || got.EventID != c.EventID) {
				t.Errorf("decodeCursor(encoded) = %+v, want %+v", got, c)
			}
		})
	}
}

func TestSortBucketsBySecondsDesc(t *testing.T) {
	tests := []struct {
		name    string
		buckets []*bucket
		wantIDs []string
	}{
		{
			name: "sorted descending by seconds",
			buckets: []*bucket{
				{ID: "a", Seconds: 10},
				{ID: "b", Seconds: 30},
				{ID: "c", Seconds: 20},
			},
			wantIDs: []string{"b", "c", "a"},
		},
		{
			name: "ties broken by id ascending",
			buckets: []*bucket{
				{ID: "z", Seconds: 10},
				{ID: "a", Seconds: 10},
			},
			wantIDs: []string{"a", "z"},
		},
		{
			name:    "empty slice",
			buckets: []*bucket{},
			wantIDs: []string{},
		},
		{
			name:    "single element",
			buckets: []*bucket{{ID: "only", Seconds: 5}},
			wantIDs: []string{"only"},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			sortBucketsBySecondsDesc(tt.buckets)
			if len(tt.buckets) != len(tt.wantIDs) {
				t.Fatalf("len = %d, want %d", len(tt.buckets), len(tt.wantIDs))
			}
			for i, b := range tt.buckets {
				if b.ID != tt.wantIDs[i] {
					t.Errorf("buckets[%d].ID = %q, want %q", i, b.ID, tt.wantIDs[i])
				}
			}
		})
	}
}

func TestDerefStr(t *testing.T) {
	s := "hello"
	tests := []struct {
		name string
		p    *string
		want string
	}{
		{name: "nil pointer", p: nil, want: ""},
		{name: "non-nil pointer", p: &s, want: "hello"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			if got := derefStr(tt.p); got != tt.want {
				t.Errorf("derefStr() = %q, want %q", got, tt.want)
			}
		})
	}
}
