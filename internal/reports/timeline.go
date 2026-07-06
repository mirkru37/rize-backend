package reports

import (
	"context"
	"fmt"
	"time"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// Timeline implements GET /v1/reports/timeline: the chronological
// "timeline report" per documentation/api-reference.md §Activities &
// reports. Unlike the other report endpoints, a timeline isn't a derived
// aggregate — it's the ordered raw activity stream itself — so no overlap
// trimming is applied here (trimming only exists to keep a *summed*
// total honest; a raw chronological view has nothing to sum). It reuses
// the same raw-event listing shape and keyset cursor convention as
// internal/activities' GET /v1/activities, duplicated in this package's
// cursor.go per this repo's existing per-package small-helper convention.
func (s *Service) Timeline(ctx context.Context, userID string, from, to time.Time, f reportFilters, rawCursor string, rawLimit int) ([]storedb.ActivityEvent, string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	if err := validateRange(from, to); err != nil {
		return nil, "", false, err
	}
	if err := f.validatePrecision(); err != nil {
		return nil, "", false, err
	}

	appID, err := parseOptionalUUID(f.AppID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: app_id must be a valid UUID", ErrValidation)
	}
	categoryID, err := parseOptionalUUID(f.CategoryID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: category_id must be a valid UUID", ErrValidation)
	}
	projectID, err := parseOptionalUUID(f.ProjectID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: project_id must be a valid UUID", ErrValidation)
	}
	deviceID, err := parseOptionalUUID(f.DeviceID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: device_id must be a valid UUID", ErrValidation)
	}
	var precision *string
	if f.Precision != "" {
		precision = &f.Precision
	}

	cur, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, "", false, err
	}

	limit := rawLimit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	rows, err := s.Queries.ListActivityEventsForUser(ctx, storedb.ListActivityEventsForUserParams{
		UserID:          uid,
		FromTs:          pgTimestamptz(from),
		ToTs:            pgTimestamptz(to),
		AppID:           appID,
		CategoryID:      categoryID,
		ProjectID:       projectID,
		DeviceID:        deviceID,
		Precision:       precision,
		CursorStartedAt: cur.timestamptz(),
		CursorEventID:   cur.uuid(),
		PageLimit:       store.LimitParam(limit + 1),
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("reports: timeline: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := rawCursor
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = encodeCursor(cursor{StartedAt: last.StartedAt.Time, EventID: last.EventID.String()})
	}
	return rows, nextCursor, hasMore, nil
}
