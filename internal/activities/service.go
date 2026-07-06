package activities

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

func pgTimestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// validPrecision mirrors activity_events.precision's CHECK constraint per
// documentation/database-schema.md, and
// documentation/sync-protocol.md §Precision Semantics.
var validPrecision = map[string]bool{"exact": true, "approximate": true}

// Service implements GET /v1/activities' business logic.
type Service struct {
	Queries storedb.Querier
}

// ListParams is GET /v1/activities' parsed, validated request.
type ListParams struct {
	From       time.Time
	To         time.Time
	AppID      string
	CategoryID string
	ProjectID  string
	DeviceID   string
	Precision  string
	Cursor     string
	Limit      int
}

// List implements GET /v1/activities: raw tracked events for userID within
// [From, To), filtered per documentation/api-reference.md §Activities &
// reports. See internal/reports for the derived-aggregate endpoints that
// read the same underlying data.
func (s *Service) List(ctx context.Context, userID string, p ListParams) ([]storedb.ActivityEvent, string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	if p.From.IsZero() || p.To.IsZero() {
		return nil, "", false, fmt.Errorf("%w: from and to are required", ErrValidation)
	}
	if err := store.ValidateRange(p.From, p.To); err != nil {
		return nil, "", false, fmt.Errorf("%w: %s", ErrValidation, err.Error())
	}

	appID, err := parseOptionalUUID(p.AppID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: app_id must be a valid UUID", ErrValidation)
	}
	categoryID, err := parseOptionalUUID(p.CategoryID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: category_id must be a valid UUID", ErrValidation)
	}
	projectID, err := parseOptionalUUID(p.ProjectID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: project_id must be a valid UUID", ErrValidation)
	}
	deviceID, err := parseOptionalUUID(p.DeviceID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: device_id must be a valid UUID", ErrValidation)
	}
	var precision *string
	if p.Precision != "" {
		if !validPrecision[p.Precision] {
			return nil, "", false, fmt.Errorf("%w: precision must be one of exact, approximate", ErrValidation)
		}
		precision = &p.Precision
	}

	cur, err := decodeCursor(p.Cursor)
	if err != nil {
		return nil, "", false, err
	}

	limit := p.Limit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	rows, err := s.Queries.ListActivityEventsForUser(ctx, storedb.ListActivityEventsForUserParams{
		UserID:          uid,
		FromTs:          pgTimestamptz(p.From),
		ToTs:            pgTimestamptz(p.To),
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
		return nil, "", false, fmt.Errorf("activities: list: %w", err)
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := p.Cursor
	if len(rows) > 0 {
		last := rows[len(rows)-1]
		nextCursor = encodeCursor(cursor{StartedAt: last.StartedAt.Time, EventID: last.EventID.String()})
	}
	return rows, nextCursor, hasMore, nil
}
