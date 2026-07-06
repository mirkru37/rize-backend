package focussessions

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store"
	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// validKind / validStatus mirror the CHECK constraints on focus_sessions
// per documentation/database-schema.md, matching internal/sync's push-side
// validFocusKind/validFocusStatus.
var (
	validKind = map[string]bool{
		"focus":   true,
		"break":   true,
		"meeting": true,
	}
	validStatus = map[string]bool{
		"running":   true,
		"completed": true,
		"abandoned": true,
	}
)

// Service implements the /v1/focus-sessions business logic.
type Service struct {
	Queries storedb.Querier
}

// resolveDevice validates that deviceID exists and belongs to uid, per
// documentation/security.md §Tenant Isolation.
func (s *Service) resolveDevice(ctx context.Context, uid pgtype.UUID, deviceIDRaw string) (pgtype.UUID, error) {
	did, err := parseUUID(deviceIDRaw)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: device_id must be a valid UUID", ErrValidation)
	}
	if _, err := s.Queries.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: did, UserID: uid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("%w: device_id does not reference a device owned by this user", ErrValidation)
		}
		return pgtype.UUID{}, fmt.Errorf("focussessions: resolve device: %w", err)
	}
	return did, nil
}

// resolveProject validates that an optional projectIDRaw, if supplied,
// exists and belongs to uid. An empty projectIDRaw returns an invalid
// (NULL) pgtype.UUID, since project_id is nullable.
func (s *Service) resolveProject(ctx context.Context, uid pgtype.UUID, projectIDRaw string) (pgtype.UUID, error) {
	pid, err := parseOptionalUUID(projectIDRaw)
	if err != nil {
		return pgtype.UUID{}, fmt.Errorf("%w: project_id must be a valid UUID", ErrValidation)
	}
	if !pid.Valid {
		return pgtype.UUID{}, nil
	}
	if _, err := s.Queries.GetProjectByIDForUser(ctx, storedb.GetProjectByIDForUserParams{ID: pid, UserID: uid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgtype.UUID{}, fmt.Errorf("%w: project_id does not reference a project owned by this user", ErrValidation)
		}
		return pgtype.UUID{}, fmt.Errorf("focussessions: resolve project: %w", err)
	}
	return pid, nil
}

func parseTimestamp(raw string) (pgtype.Timestamptz, error) {
	t, err := time.Parse(timeLayout, raw)
	if err != nil {
		return pgtype.Timestamptz{}, fmt.Errorf("%w: expected an RFC3339 timestamp, got %q", ErrValidation, raw)
	}
	return pgtype.Timestamptz{Time: t, Valid: true}, nil
}

// Create implements POST /v1/focus-sessions.
func (s *Service) Create(ctx context.Context, userID string, req createRequest) (storedb.FocusSession, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.FocusSession{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	deviceID, err := s.resolveDevice(ctx, uid, req.DeviceID)
	if err != nil {
		return storedb.FocusSession{}, err
	}
	projectID, err := s.resolveProject(ctx, uid, req.ProjectID)
	if err != nil {
		return storedb.FocusSession{}, err
	}
	if !validKind[req.Kind] {
		return storedb.FocusSession{}, fmt.Errorf("%w: kind must be one of focus, break, meeting", ErrValidation)
	}
	if !validStatus[req.Status] {
		return storedb.FocusSession{}, fmt.Errorf("%w: status must be one of running, completed, abandoned", ErrValidation)
	}
	startedAt, err := parseTimestamp(req.StartedAt)
	if err != nil {
		return storedb.FocusSession{}, err
	}
	var endedAt pgtype.Timestamptz
	if req.EndedAt != "" {
		endedAt, err = parseTimestamp(req.EndedAt)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	}

	var id pgtype.UUID
	if req.ID != "" {
		id, err = parseUUID(req.ID)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	} else {
		id, err = newUUIDv4()
		if err != nil {
			return storedb.FocusSession{}, fmt.Errorf("focussessions: %w", err)
		}
	}

	var note *string
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		note = &trimmed
	}

	created, err := s.Queries.CreateFocusSessionForUser(ctx, storedb.CreateFocusSessionForUserParams{
		ID:               id,
		UserID:           uid,
		DeviceID:         deviceID,
		ProjectID:        projectID,
		Kind:             req.Kind,
		PlannedDurationS: req.PlannedDurationS,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		Status:           req.Status,
		Note:             note,
	})
	if err != nil {
		if _, message, ok := store.ConstraintViolation(err); ok {
			return storedb.FocusSession{}, fmt.Errorf("%w: %s", ErrValidation, message)
		}
		return storedb.FocusSession{}, fmt.Errorf("focussessions: create: %w", err)
	}
	return created, nil
}

// List implements GET /v1/focus-sessions.
func (s *Service) List(ctx context.Context, userID, rawCursor string, rawLimit int) ([]storedb.FocusSession, string, bool, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	cursor, err := store.DecodeCursor(rawCursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("%w: invalid cursor", ErrValidation)
	}
	limit := rawLimit
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}

	rows, err := s.Queries.ListFocusSessionsForUser(ctx, storedb.ListFocusSessionsForUserParams{
		UserID:    uid,
		ServerSeq: cursor,
		Limit:     store.LimitParam(limit + 1),
	})
	if err != nil {
		return nil, "", false, fmt.Errorf("focussessions: list: %w", err)
	}
	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	nextCursor := cursor
	if len(rows) > 0 {
		nextCursor = rows[len(rows)-1].ServerSeq
	}
	return rows, store.EncodeCursor(nextCursor), hasMore, nil
}

// Get implements GET /v1/focus-sessions/{id}.
func (s *Service) Get(ctx context.Context, userID, sessionID string) (storedb.FocusSession, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.FocusSession{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	sid, err := parseUUID(sessionID)
	if err != nil {
		return storedb.FocusSession{}, ErrNotFound
	}
	session, err := s.Queries.GetFocusSessionForUser(ctx, storedb.GetFocusSessionForUserParams{ID: sid, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.FocusSession{}, ErrNotFound
		}
		return storedb.FocusSession{}, fmt.Errorf("focussessions: get: %w", err)
	}
	return session, nil
}

// Update implements PATCH /v1/focus-sessions/{id}.
func (s *Service) Update(ctx context.Context, userID, sessionID string, req updateRequest) (storedb.FocusSession, error) {
	uid, err := parseUUID(userID)
	if err != nil {
		return storedb.FocusSession{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	current, err := s.Get(ctx, userID, sessionID)
	if err != nil {
		return storedb.FocusSession{}, err
	}

	deviceID := current.DeviceID
	if req.DeviceID != nil {
		deviceID, err = s.resolveDevice(ctx, uid, *req.DeviceID)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	}

	projectID := current.ProjectID
	switch {
	case req.ClearProjectID:
		projectID = pgtype.UUID{}
	case req.ProjectID != nil:
		projectID, err = s.resolveProject(ctx, uid, *req.ProjectID)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	}

	kind := current.Kind
	if req.Kind != nil {
		if !validKind[*req.Kind] {
			return storedb.FocusSession{}, fmt.Errorf("%w: kind must be one of focus, break, meeting", ErrValidation)
		}
		kind = *req.Kind
	}

	status := current.Status
	if req.Status != nil {
		if !validStatus[*req.Status] {
			return storedb.FocusSession{}, fmt.Errorf("%w: status must be one of running, completed, abandoned", ErrValidation)
		}
		status = *req.Status
	}

	plannedDurationS := current.PlannedDurationS
	if req.PlannedDurationS != nil {
		plannedDurationS = req.PlannedDurationS
	}

	startedAt := current.StartedAt
	if req.StartedAt != nil {
		startedAt, err = parseTimestamp(*req.StartedAt)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	}

	endedAt := current.EndedAt
	switch {
	case req.ClearEndedAt:
		endedAt = pgtype.Timestamptz{}
	case req.EndedAt != nil:
		endedAt, err = parseTimestamp(*req.EndedAt)
		if err != nil {
			return storedb.FocusSession{}, err
		}
	}

	note := current.Note
	if req.Note != nil {
		trimmed := strings.TrimSpace(*req.Note)
		note = &trimmed
	}

	sid, _ := parseUUID(sessionID)

	updated, err := s.Queries.UpdateFocusSessionForUser(ctx, storedb.UpdateFocusSessionForUserParams{
		ID:               sid,
		UserID:           uid,
		DeviceID:         deviceID,
		ProjectID:        projectID,
		Kind:             kind,
		PlannedDurationS: plannedDurationS,
		StartedAt:        startedAt,
		EndedAt:          endedAt,
		Status:           status,
		Note:             note,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storedb.FocusSession{}, ErrNotFound
		}
		if _, message, ok := store.ConstraintViolation(err); ok {
			return storedb.FocusSession{}, fmt.Errorf("%w: %s", ErrValidation, message)
		}
		return storedb.FocusSession{}, fmt.Errorf("focussessions: update: %w", err)
	}
	return updated, nil
}

// Delete implements DELETE /v1/focus-sessions/{id}.
func (s *Service) Delete(ctx context.Context, userID, sessionID string) error {
	uid, err := parseUUID(userID)
	if err != nil {
		return fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}
	sid, err := parseUUID(sessionID)
	if err != nil {
		return ErrNotFound
	}
	rows, err := s.Queries.SoftDeleteFocusSessionForUser(ctx, storedb.SoftDeleteFocusSessionForUserParams{ID: sid, UserID: uid})
	if err != nil {
		return fmt.Errorf("focussessions: delete: %w", err)
	}
	if rows == 0 {
		return ErrNotFound
	}
	return nil
}
