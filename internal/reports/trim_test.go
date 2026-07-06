package reports

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm
}

func TestPerDeviceSecondsSameDeviceOverlapCapped(t *testing.T) {
	windowStart := mustParse(t, "2026-07-01T00:00:00Z")
	windowEnd := mustParse(t, "2026-07-02T00:00:00Z")

	// Two overlapping intervals from the SAME device: 09:00-09:30 and
	// 09:15-09:45. Naive summing would give 30m + 30m = 60m; trimmed, the
	// merged coverage is 09:00-09:45 = 45m.
	byDevice := map[string][]interval{
		"device-a": {
			{start: mustParse(t, "2026-07-01T09:00:00Z"), end: mustParse(t, "2026-07-01T09:30:00Z")},
			{start: mustParse(t, "2026-07-01T09:15:00Z"), end: mustParse(t, "2026-07-01T09:45:00Z")},
		},
	}

	got := perDeviceSeconds(windowStart, windowEnd, byDevice)
	want := int64(45 * 60)
	if got != want {
		t.Errorf("perDeviceSeconds() = %d, want %d (same-device overlap should be capped, not summed)", got, want)
	}
}

func TestPerDeviceSecondsCrossDeviceOverlapNotCapped(t *testing.T) {
	windowStart := mustParse(t, "2026-07-01T00:00:00Z")
	windowEnd := mustParse(t, "2026-07-02T00:00:00Z")

	// Two overlapping intervals from DIFFERENT devices: both should count
	// in full, since simultaneous desktop+mobile activity is legitimate.
	byDevice := map[string][]interval{
		"device-a": {{start: mustParse(t, "2026-07-01T09:00:00Z"), end: mustParse(t, "2026-07-01T09:30:00Z")}},
		"device-b": {{start: mustParse(t, "2026-07-01T09:15:00Z"), end: mustParse(t, "2026-07-01T09:45:00Z")}},
	}

	got := perDeviceSeconds(windowStart, windowEnd, byDevice)
	want := int64(30*60 + 30*60)
	if got != want {
		t.Errorf("perDeviceSeconds() = %d, want %d (cross-device overlap must not be trimmed)", got, want)
	}
}

func TestPerDeviceSecondsClipsToWindow(t *testing.T) {
	windowStart := mustParse(t, "2026-07-01T00:00:00Z")
	windowEnd := mustParse(t, "2026-07-02T00:00:00Z")

	// Event starts before the window and ends after it: only the portion
	// inside the window should count.
	byDevice := map[string][]interval{
		"device-a": {{start: mustParse(t, "2026-06-30T23:00:00Z"), end: mustParse(t, "2026-07-01T01:00:00Z")}},
	}

	got := perDeviceSeconds(windowStart, windowEnd, byDevice)
	want := int64(60 * 60)
	if got != want {
		t.Errorf("perDeviceSeconds() = %d, want %d (interval should be clipped to the window)", got, want)
	}
}

func TestPerDeviceSecondsNonOverlappingSumsPlainly(t *testing.T) {
	windowStart := mustParse(t, "2026-07-01T00:00:00Z")
	windowEnd := mustParse(t, "2026-07-02T00:00:00Z")

	byDevice := map[string][]interval{
		"device-a": {
			{start: mustParse(t, "2026-07-01T09:00:00Z"), end: mustParse(t, "2026-07-01T09:10:00Z")},
			{start: mustParse(t, "2026-07-01T10:00:00Z"), end: mustParse(t, "2026-07-01T10:10:00Z")},
		},
	}

	got := perDeviceSeconds(windowStart, windowEnd, byDevice)
	want := int64(10*60 + 10*60)
	if got != want {
		t.Errorf("perDeviceSeconds() = %d, want %d", got, want)
	}
}
