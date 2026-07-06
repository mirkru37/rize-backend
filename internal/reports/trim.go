package reports

import (
	"sort"
	"time"
)

// interval is a clipped, half-open [start, end) time span belonging to one
// device, used by trimSeconds' overlap-capping pass.
type interval struct {
	start time.Time
	end   time.Time
}

// clipToWindow clips every interval to [windowStart, windowEnd), dropping
// any interval that ends up empty or inverted (start >= end) once clipped.
func clipToWindow(windowStart, windowEnd time.Time, events []interval) []interval {
	clipped := make([]interval, 0, len(events))
	for _, ev := range events {
		start := ev.start
		if start.Before(windowStart) {
			start = windowStart
		}
		end := ev.end
		if end.After(windowEnd) {
			end = windowEnd
		}
		if !start.Before(end) {
			continue
		}
		clipped = append(clipped, interval{start: start, end: end})
	}
	return clipped
}

// mergeAndSum sorts events by start time and merges overlapping/adjacent
// intervals, returning the total covered duration in whole seconds. It
// assumes every interval already belongs to the same device — merging
// across devices is exactly the cross-device capping this package must
// NOT apply per the Overlap Rules above.
func mergeAndSum(events []interval) int64 {
	if len(events) == 0 {
		return 0
	}
	sort.Slice(events, func(i, j int) bool { return events[i].start.Before(events[j].start) })

	var total int64
	curStart, curEnd := events[0].start, events[0].end
	for _, ev := range events[1:] {
		if ev.start.After(curEnd) {
			total += int64(curEnd.Sub(curStart).Seconds())
			curStart, curEnd = ev.start, ev.end
			continue
		}
		if ev.end.After(curEnd) {
			curEnd = ev.end
		}
	}
	total += int64(curEnd.Sub(curStart).Seconds())
	return total
}

// perDeviceSeconds implements the report-query layer's raw-event overlap
// trimming, per documentation/architecture-backend.md §Aggregation
// Strategy and documentation/sync-protocol.md §Overlap Rules, for one
// dimension bucket (e.g. "this category's events across the window"):
//
//   - Same-device overlapping intervals are capped so a single device
//     never contributes more active time to the window than the window
//     itself contains: byDevice's events are clipped to [windowStart,
//     windowEnd), sorted by start time, and merged (overlapping/adjacent
//     intervals combined) before summing each device's now
//     non-overlapping durations.
//   - Overlapping intervals from *different* devices are not trimmed
//     against each other: each device's merged total is summed into the
//     bucket total independently, so genuinely simultaneous desktop and
//     mobile activity both count.
//
// byDevice keys are device_id strings; callers group raw rows by
// dimension value first (see service.go) and call this once per bucket.
func perDeviceSeconds(windowStart, windowEnd time.Time, byDevice map[string][]interval) int64 {
	var total int64
	for _, events := range byDevice {
		total += mergeAndSum(clipToWindow(windowStart, windowEnd, events))
	}
	return total
}
