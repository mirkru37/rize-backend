package reports

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// dimension selects which column raw-event rows are grouped by when
// computing a report's per-bucket trimmed totals.
type dimension int

const (
	dimCategory dimension = iota
	dimApp
	dimProject
)

// bucket is one grouped row of a dimensioned report (one category, app, or
// project) with its overlap-trimmed total.
type bucket struct {
	ID       string // "" means "no category/app/project" (see Name)
	Name     string // e.g. "Uncategorized", "Unknown", "No Project" when ID == ""
	BundleID string // apps only
	Seconds  int64
}

func mergeBucket(dst map[string]*bucket, id, name, bundleID string, seconds int64) {
	b, ok := dst[id]
	if !ok {
		b = &bucket{ID: id}
		dst[id] = b
	}
	if name != "" {
		b.Name = name
	}
	if bundleID != "" {
		b.BundleID = bundleID
	}
	b.Seconds += seconds
}

// sortBucketsBySecondsDesc sorts buckets by descending trimmed total,
// breaking ties by ID for deterministic response ordering (largest
// time-consumer first is the natural read order for a breakdown report).
func sortBucketsBySecondsDesc(buckets []*bucket) {
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].Seconds != buckets[j].Seconds {
			return buckets[i].Seconds > buckets[j].Seconds
		}
		return buckets[i].ID < buckets[j].ID
	})
}

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// windowSeconds is the closed-period cagg queries' device-overlap cap
// bound: the length, in seconds, of a closed window, used by
// internal/store/queries/activities.sql's CategoryTotalsForRange and
// AppTotalsForRange as an UPPER BOUND on how much same-device overlap a
// device's total_s can be inflated by. It is not the same computation as
// perDeviceSeconds' interval merge for the raw path: that clips and
// merges individual event intervals, so it always returns each device's
// exact non-overlapping covered duration; this instead caps an
// already-summed total_s at the window's length, which only catches
// overlap severe enough to push the naive sum above the window itself —
// see CategoryTotalsForRange's doc comment for a worked example where the
// two disagree. The two callers only ever invoke this with a genuinely
// closed (non-empty, from-before-to) window per splitClosedOpen, but the
// callers pass the result straight into LEAST(device_total_s,
// window_seconds) in SQL, where a zero or negative value would silently
// zero out every total — so this defends that invariant explicitly rather
// than relying on the caller never regressing it.
func windowSeconds(w window) int64 {
	seconds := int64(w.To.Sub(w.From).Seconds())
	if seconds < 1 {
		seconds = 1
	}
	return seconds
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// rawTotals runs the report layer's raw-event pass over [from, to) for one
// dimension, per documentation/architecture-backend.md §Aggregation
// Strategy: groups matching activity_events by the dimension's column,
// then within each bucket caps same-device overlap and disambiguates
// cross-device overlap via trim.go's perDeviceSeconds, per
// documentation/sync-protocol.md §Overlap Rules.
func (s *Service) rawTotals(ctx context.Context, uid pgtype.UUID, from, to time.Time, f reportFilters, dim dimension) (map[string]*bucket, error) {
	appID, err := parseOptionalUUID(f.AppID)
	if err != nil {
		return nil, fmt.Errorf("%w: app_id must be a valid UUID", ErrValidation)
	}
	categoryID, err := parseOptionalUUID(f.CategoryID)
	if err != nil {
		return nil, fmt.Errorf("%w: category_id must be a valid UUID", ErrValidation)
	}
	projectID, err := parseOptionalUUID(f.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("%w: project_id must be a valid UUID", ErrValidation)
	}
	deviceID, err := parseOptionalUUID(f.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("%w: device_id must be a valid UUID", ErrValidation)
	}
	var precision *string
	if f.Precision != "" {
		if !validPrecision[f.Precision] {
			return nil, fmt.Errorf("%w: precision must be one of exact, approximate", ErrValidation)
		}
		precision = &f.Precision
	}

	rows, err := s.Queries.RawActivityEventsForReport(ctx, storedb.RawActivityEventsForReportParams{
		UserID:     uid,
		FromTs:     pgTimestamptz(from),
		ToTs:       pgTimestamptz(to),
		AppID:      appID,
		CategoryID: categoryID,
		ProjectID:  projectID,
		DeviceID:   deviceID,
		Precision:  precision,
	})
	if err != nil {
		return nil, fmt.Errorf("reports: raw totals: %w", err)
	}

	type nameInfo struct{ name, bundle string }
	names := map[string]nameInfo{}
	grouped := map[string]map[string][]interval{}

	for _, r := range rows {
		var id, name, bundle string
		switch dim {
		case dimCategory:
			if r.CategoryID.Valid {
				id, name = r.CategoryID.String(), derefStr(r.CategoryName)
			} else {
				name = "Uncategorized"
			}
		case dimApp:
			if r.AppID.Valid {
				id, name, bundle = r.AppID.String(), derefStr(r.AppName), derefStr(r.AppBundleID)
			} else {
				name = "Unknown"
			}
		case dimProject:
			if r.ProjectID.Valid {
				id, name = r.ProjectID.String(), derefStr(r.ProjectName)
			} else {
				name = "No Project"
			}
		}
		if grouped[id] == nil {
			grouped[id] = map[string][]interval{}
		}
		dev := r.DeviceID.String()
		grouped[id][dev] = append(grouped[id][dev], interval{start: r.StartedAt.Time, end: r.EndedAt.Time})
		names[id] = nameInfo{name, bundle}
	}

	result := make(map[string]*bucket, len(grouped))
	for id, byDevice := range grouped {
		seconds := perDeviceSeconds(from, to, byDevice)
		info := names[id]
		result[id] = &bucket{ID: id, Name: info.name, BundleID: info.bundle, Seconds: seconds}
	}
	return result, nil
}

// categoryTotals returns overlap-trimmed per-category totals over
// [from, to), combining the daily_category_totals continuous aggregate
// for closed days with a raw-event pass for the current/partial day, per
// documentation/architecture-backend.md §Aggregation Strategy.
//
// The cagg fast path is only used when the request has no app_id,
// project_id, device_id, or precision filter: daily_category_totals
// carries none of those dimensions (see
// documentation/database-schema.md's `daily_category_totals(user_id, day,
// category_id, total_s)`), so a request scoped by any of them cannot be
// served by the aggregate and falls back to the raw pass for the entire
// range. A category_id filter is compatible with the cagg path (applied
// as a post-aggregation key filter below) since category_id is the
// aggregate's own grouping column.
func (s *Service) categoryTotals(ctx context.Context, uid pgtype.UUID, from, to time.Time, f reportFilters) (map[string]*bucket, error) {
	result := map[string]*bucket{}

	if f.AppID != "" || f.ProjectID != "" || f.DeviceID != "" || f.Precision != "" {
		raw, err := s.rawTotals(ctx, uid, from, to, f, dimCategory)
		if err != nil {
			return nil, err
		}
		return raw, nil
	}

	closed, hasClosed, rawWindows := splitClosedOpen(from, to, time.Now())
	if hasClosed {
		rows, err := s.Queries.CategoryTotalsForRange(ctx, storedb.CategoryTotalsForRangeParams{
			UserID:        uid,
			FromDay:       pgTimestamptz(closed.From),
			ToDay:         pgTimestamptz(closed.To),
			WindowSeconds: windowSeconds(closed),
		})
		if err != nil {
			return nil, fmt.Errorf("reports: category totals: %w", err)
		}
		for _, row := range rows {
			id, name := "", "Uncategorized"
			if row.CategoryID.Valid {
				id, name = row.CategoryID.String(), derefStr(row.CategoryName)
			}
			mergeBucket(result, id, name, "", row.TotalS)
		}
	}
	for _, w := range rawWindows {
		raw, err := s.rawTotals(ctx, uid, w.From, w.To, f, dimCategory)
		if err != nil {
			return nil, err
		}
		for id, b := range raw {
			mergeBucket(result, id, b.Name, b.BundleID, b.Seconds)
		}
	}

	if f.CategoryID != "" {
		id, err := parseUUID(f.CategoryID)
		if err != nil {
			return nil, fmt.Errorf("%w: category_id must be a valid UUID", ErrValidation)
		}
		filtered := map[string]*bucket{}
		if b, ok := result[id.String()]; ok {
			filtered[id.String()] = b
		}
		return filtered, nil
	}
	return result, nil
}

// appTotals mirrors categoryTotals for the daily_app_totals aggregate; see
// its doc comment for the cagg-compatibility rule (here: no category_id,
// project_id, device_id, or precision filter).
func (s *Service) appTotals(ctx context.Context, uid pgtype.UUID, from, to time.Time, f reportFilters) (map[string]*bucket, error) {
	result := map[string]*bucket{}

	if f.CategoryID != "" || f.ProjectID != "" || f.DeviceID != "" || f.Precision != "" {
		return s.rawTotals(ctx, uid, from, to, f, dimApp)
	}

	closed, hasClosed, rawWindows := splitClosedOpen(from, to, time.Now())
	if hasClosed {
		rows, err := s.Queries.AppTotalsForRange(ctx, storedb.AppTotalsForRangeParams{
			UserID:        uid,
			FromDay:       pgTimestamptz(closed.From),
			ToDay:         pgTimestamptz(closed.To),
			WindowSeconds: windowSeconds(closed),
		})
		if err != nil {
			return nil, fmt.Errorf("reports: app totals: %w", err)
		}
		for _, row := range rows {
			id, name, bundle := "", "Unknown", ""
			if row.AppID.Valid {
				id, name, bundle = row.AppID.String(), derefStr(row.AppName), derefStr(row.AppBundleID)
			}
			mergeBucket(result, id, name, bundle, row.TotalS)
		}
	}
	for _, w := range rawWindows {
		raw, err := s.rawTotals(ctx, uid, w.From, w.To, f, dimApp)
		if err != nil {
			return nil, err
		}
		for id, b := range raw {
			mergeBucket(result, id, b.Name, b.BundleID, b.Seconds)
		}
	}

	if f.AppID != "" {
		id, err := parseUUID(f.AppID)
		if err != nil {
			return nil, fmt.Errorf("%w: app_id must be a valid UUID", ErrValidation)
		}
		filtered := map[string]*bucket{}
		if b, ok := result[id.String()]; ok {
			filtered[id.String()] = b
		}
		return filtered, nil
	}
	return result, nil
}

// projectTotals has no supporting continuous aggregate at all — see
// service.go's package-level doc comment for why reports/projects always
// runs the raw-event pass over the full requested range, for both closed
// and open periods alike.
func (s *Service) projectTotals(ctx context.Context, uid pgtype.UUID, from, to time.Time, f reportFilters) (map[string]*bucket, error) {
	return s.rawTotals(ctx, uid, from, to, f, dimProject)
}
