package reports

import "time"

// window is a half-open [From, To) time span.
type window struct {
	From time.Time
	To   time.Time
}

func dayStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// splitClosedOpen resolves documentation/architecture-backend.md
// §Aggregation Strategy's closed-vs-current-period split for the range
// [from, to): "today" (now, in UTC — see the RIZ-35 brief's timezone
// assumption note) is never a closed period, so any portion of the
// requested range that falls on or after todayStart must be read from
// raw activity_events, not a continuous aggregate.
//
// The continuous aggregates only materialize whole-day buckets, so this
// also aligns the closed portion to UTC day boundaries: a `from`/`to` that
// doesn't fall exactly at midnight yields a leading and/or trailing raw
// segment for the partial day(s) at the edges, in addition to the raw
// segment covering "today". The three pieces this returns
// (closed, plus 0-2 raw segments) are always disjoint and always cover
// [from, to) exactly, so summing their totals never double-counts.
func splitClosedOpen(from, to, now time.Time) (closed window, hasClosed bool, raw []window) {
	todayStart := dayStart(now)

	effectiveClosedTo := to
	if todayStart.Before(effectiveClosedTo) {
		effectiveClosedTo = todayStart
	}
	if !effectiveClosedTo.After(from) {
		// The entire range is "today" (or later) — no closed portion at all.
		raw = append(raw, window{From: from, To: to})
		return closed, false, raw
	}

	alignedFrom := dayStart(from)
	if !from.Equal(alignedFrom) {
		alignedFrom = alignedFrom.AddDate(0, 0, 1)
		raw = append(raw, window{From: from, To: minTime(to, alignedFrom)})
	}

	alignedTo := dayStart(effectiveClosedTo)

	if alignedFrom.Before(alignedTo) {
		closed = window{From: alignedFrom, To: alignedTo}
		hasClosed = true
	}

	if to.After(alignedTo) {
		start := alignedTo
		if !hasClosed && start.Before(alignedFrom) {
			start = alignedFrom
		}
		raw = append(raw, window{From: start, To: to})
	}

	return closed, hasClosed, raw
}
