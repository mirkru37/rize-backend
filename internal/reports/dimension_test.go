package reports

import (
	"testing"
)

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
