package reports

import (
	"testing"
	"time"
)

func TestSplitClosedOpenWholeRangeInThePast(t *testing.T) {
	now := mustParse(t, "2026-07-10T15:00:00Z")
	from := mustParse(t, "2026-07-01T00:00:00Z")
	to := mustParse(t, "2026-07-05T00:00:00Z")

	closed, hasClosed, raw := splitClosedOpen(from, to, now)
	if !hasClosed {
		t.Fatalf("expected a closed segment")
	}
	if !closed.From.Equal(from) || !closed.To.Equal(to) {
		t.Errorf("closed = %v..%v, want %v..%v", closed.From, closed.To, from, to)
	}
	if len(raw) != 0 {
		t.Errorf("expected no raw segments, got %v", raw)
	}
}

func TestSplitClosedOpenWholeRangeIsToday(t *testing.T) {
	now := mustParse(t, "2026-07-10T15:00:00Z")
	from := mustParse(t, "2026-07-10T09:00:00Z")
	to := mustParse(t, "2026-07-10T12:00:00Z")

	closed, hasClosed, raw := splitClosedOpen(from, to, now)
	if hasClosed {
		t.Errorf("expected no closed segment, got %v", closed)
	}
	if len(raw) != 1 || !raw[0].From.Equal(from) || !raw[0].To.Equal(to) {
		t.Errorf("raw = %v, want single segment %v..%v", raw, from, to)
	}
}

func TestSplitClosedOpenSpansIntoToday(t *testing.T) {
	now := mustParse(t, "2026-07-10T15:00:00Z")
	from := mustParse(t, "2026-07-01T00:00:00Z")
	to := mustParse(t, "2026-07-10T09:00:00Z")
	todayStart := mustParse(t, "2026-07-10T00:00:00Z")

	closed, hasClosed, raw := splitClosedOpen(from, to, now)
	if !hasClosed {
		t.Fatalf("expected a closed segment for the full days before today")
	}
	if !closed.From.Equal(from) || !closed.To.Equal(todayStart) {
		t.Errorf("closed = %v..%v, want %v..%v", closed.From, closed.To, from, todayStart)
	}
	if len(raw) != 1 || !raw[0].From.Equal(todayStart) || !raw[0].To.Equal(to) {
		t.Errorf("raw = %v, want single segment %v..%v", raw, todayStart, to)
	}
}

func TestSplitClosedOpenUnalignedFromLeavesLeadingRawSegment(t *testing.T) {
	now := mustParse(t, "2026-07-10T15:00:00Z")
	from := mustParse(t, "2026-07-01T12:00:00Z") // mid-day, not aligned to midnight
	to := mustParse(t, "2026-07-05T00:00:00Z")
	dayTwo := mustParse(t, "2026-07-02T00:00:00Z")

	closed, hasClosed, raw := splitClosedOpen(from, to, now)
	if !hasClosed {
		t.Fatalf("expected a closed segment for the whole aligned days")
	}
	if !closed.From.Equal(dayTwo) || !closed.To.Equal(to) {
		t.Errorf("closed = %v..%v, want %v..%v", closed.From, closed.To, dayTwo, to)
	}
	if len(raw) != 1 || !raw[0].From.Equal(from) || !raw[0].To.Equal(dayTwo) {
		t.Errorf("raw = %v, want a single leading segment %v..%v", raw, from, dayTwo)
	}
}

func TestSplitClosedOpenSegmentsAreDisjointAndCoverTheWholeRange(t *testing.T) {
	now := mustParse(t, "2026-07-10T15:00:00Z")
	from := mustParse(t, "2026-07-01T12:00:00Z")
	to := mustParse(t, "2026-07-10T09:00:00Z")

	closed, hasClosed, raw := splitClosedOpen(from, to, now)

	var pieces []window
	if hasClosed {
		pieces = append(pieces, closed)
	}
	pieces = append(pieces, raw...)

	// Every piece must be non-empty, and consecutive pieces (sorted by
	// From) must exactly abut with no gap and no overlap.
	for i := 0; i < len(pieces); i++ {
		if !pieces[i].From.Before(pieces[i].To) {
			t.Errorf("piece %d is empty or inverted: %v", i, pieces[i])
		}
	}
	total := time.Duration(0)
	for _, p := range pieces {
		total += p.To.Sub(p.From)
	}
	if want := to.Sub(from); total != want {
		t.Errorf("pieces cover %v total, want %v (gap or overlap present)", total, want)
	}
}
