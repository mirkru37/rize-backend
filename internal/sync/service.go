package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/mirkru37/rize-backend/internal/store/storedb"
)

// maxBatchItems is the batch-size limit mandated by
// documentation/sync-protocol.md §Push ("A single request MUST NOT
// contain more than 500 items").
const maxBatchItems = 500

// maxClockSkew is the threshold past which a client-supplied `updated_at`
// on a mutable (LWW) entity is rejected in favor of server time, per
// documentation/sync-protocol.md §Edge Cases §Clock Skew.
const maxClockSkew = 24 * time.Hour

const timeLayout = time.RFC3339

var validActivityType = map[string]bool{
	"app_active":   true,
	"idle":         true,
	"locked":       true,
	"mobile_usage": true,
	"manual":       true,
}

var validPrecision = map[string]bool{
	"exact":       true,
	"approximate": true,
}

var validFocusKind = map[string]bool{
	"focus":   true,
	"break":   true,
	"meeting": true,
}

var validFocusStatus = map[string]bool{
	"running":   true,
	"completed": true,
	"abandoned": true,
}

// platformSource maps a device's platform (documentation/database-schema.md
// devices.platform: macos/ios) to the activity_events.source enum
// (desktop/mobile/manual). See dto.go's activityEventData doc comment for
// why `source` is derived server-side rather than accepted on the wire.
var platformSource = map[string]string{
	"macos": "desktop",
	"ios":   "mobile",
}

// Service implements the push half of the sync protocol
// (documentation/sync-protocol.md §Push), per
// documentation/architecture-backend.md §Ingestion Pipeline.
type Service struct {
	Queries storedb.Querier

	// now is overridable in tests to exercise clock-skew handling
	// deterministically; defaults to time.Now when nil.
	now func() time.Time
}

func (s *Service) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

// Push validates and applies a batch of client-pushed items on behalf of
// userID (taken from the authenticated access token — never from the
// request body, per documentation/security.md §Tenant Isolation), scoping
// every write through the device identified by req.DeviceID once that
// device has been verified to belong to userID.
func (s *Service) push(ctx context.Context, userID string, req pushRequest) (pushResponse, error) {
	if len(req.Items) > maxBatchItems {
		return pushResponse{}, ErrBatchTooLarge
	}

	uid, err := parseUUID(userID)
	if err != nil {
		return pushResponse{}, fmt.Errorf("%w: invalid authenticated user id", ErrValidation)
	}

	deviceID, err := parseUUID(req.DeviceID)
	if err != nil {
		return pushResponse{}, ErrDeviceNotFound
	}

	// Scoped by user_id: a device_id that exists but belongs to a
	// different user resolves to no rows here, which is exactly the
	// tenant-isolation behavior required by the brief ("authenticated
	// user A cannot write to user B's device/events even if body claims
	// otherwise").
	device, err := s.Queries.GetDeviceByID(ctx, storedb.GetDeviceByIDParams{ID: deviceID, UserID: uid})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pushResponse{}, ErrDeviceNotFound
		}
		return pushResponse{}, fmt.Errorf("sync: resolve device: %w", err)
	}

	source, ok := platformSource[device.Platform]
	if !ok {
		source = "manual"
	}

	results := make([]pushResult, 0, len(req.Items))
	for index, item := range req.Items {
		var (
			result   pushResult
			applyErr error
		)
		switch item.EntityType {
		case "activity_event":
			result, applyErr = s.applyActivityEvent(ctx, uid, deviceID, device.Platform, source, index, item.Data)
		case "focus_session":
			result, applyErr = s.applyFocusSession(ctx, uid, deviceID, index, item.Data)
		default:
			result = invalidUnsupportedEntity(index, item.EntityType)
		}
		if applyErr != nil {
			return pushResponse{}, fmt.Errorf("sync: apply item %d (%s): %w", index, item.EntityType, applyErr)
		}
		results = append(results, result)
	}

	return pushResponse{Results: results}, nil
}

func (s *Service) applyActivityEvent(ctx context.Context, userID, deviceID pgtype.UUID, platform, source string, index int, raw json.RawMessage) (pushResult, error) {
	var data activityEventData
	if err := json.Unmarshal(raw, &data); err != nil {
		return invalidActivityEvent(index, "", "VALIDATION_ERROR", "malformed activity_event payload"), nil
	}

	eventID, err := parseUUID(data.EventID)
	if err != nil {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "event_id must be a valid UUID"), nil
	}
	startedAt, err := time.Parse(timeLayout, data.StartedAt)
	if err != nil {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "started_at must be an RFC3339 timestamp"), nil
	}
	endedAt, err := time.Parse(timeLayout, data.EndedAt)
	if err != nil {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "ended_at must be an RFC3339 timestamp"), nil
	}
	if endedAt.Before(startedAt) {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "ended_at precedes started_at"), nil
	}
	if data.AppBundleID == "" {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "app_bundle_id is required"), nil
	}
	if !validPrecision[data.Precision] {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "precision must be \"exact\" or \"approximate\""), nil
	}
	if !validActivityType[data.Type] {
		return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "type must be one of app_active, idle, locked, mobile_usage, manual"), nil
	}

	app, err := s.resolveApp(ctx, data.AppBundleID, platform)
	if err != nil {
		return pushResult{}, fmt.Errorf("resolve app: %w", err)
	}

	categoryID, err := s.resolveCategory(ctx, userID, app)
	if err != nil {
		return pushResult{}, fmt.Errorf("resolve category: %w", err)
	}

	_, err = s.Queries.InsertActivityEvent(ctx, storedb.InsertActivityEventParams{
		EventID:     eventID,
		UserID:      userID,
		DeviceID:    deviceID,
		StartedAt:   pgtype.Timestamptz{Time: startedAt, Valid: true},
		EndedAt:     pgtype.Timestamptz{Time: endedAt, Valid: true},
		Type:        data.Type,
		Source:      source,
		Precision:   data.Precision,
		AppID:       app.ID,
		RawBundleID: &data.AppBundleID,
		WindowTitle: data.WindowTitle,
		Url:         nil,
		CategoryID:  categoryID,
		ProjectID:   pgtype.UUID{},
		Deleted:     data.Deleted,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// ON CONFLICT DO NOTHING found an existing row under this
			// idempotency key: a no-op replay of an already-ingested
			// event, per documentation/sync-protocol.md §Entity Classes.
			return duplicateActivityEvent(index, data.EventID), nil
		}
		return pushResult{}, fmt.Errorf("insert activity event: %w", err)
	}

	// server_seq intentionally not echoed for activity_event, see dto.go.
	return appliedActivityEvent(index, data.EventID), nil
}

func (s *Service) applyFocusSession(ctx context.Context, userID, deviceID pgtype.UUID, index int, raw json.RawMessage) (pushResult, error) {
	var data focusSessionData
	if err := json.Unmarshal(raw, &data); err != nil {
		return invalidFocusSession(index, "", "VALIDATION_ERROR", "malformed focus_session payload"), nil
	}

	id, err := parseUUID(data.ID)
	if err != nil {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "id must be a valid UUID"), nil
	}
	updatedAt, err := time.Parse(timeLayout, data.UpdatedAt)
	if err != nil {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "updated_at must be an RFC3339 timestamp"), nil
	}
	startedAt, err := time.Parse(timeLayout, data.StartedAt)
	if err != nil {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "started_at must be an RFC3339 timestamp"), nil
	}
	var endedAt pgtype.Timestamptz
	if data.EndedAt != nil {
		t, err := time.Parse(timeLayout, *data.EndedAt)
		if err != nil {
			return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "ended_at must be an RFC3339 timestamp"), nil
		}
		endedAt = pgtype.Timestamptz{Time: t, Valid: true}
	}
	projectID, err := parseOptionalUUID(data.ProjectID)
	if err != nil {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "project_id must be a valid UUID"), nil
	}
	if !validFocusKind[data.Kind] {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "kind must be one of focus, break, meeting"), nil
	}
	if !validFocusStatus[data.Status] {
		return invalidFocusSession(index, data.ID, "VALIDATION_ERROR", "status must be one of running, completed, abandoned"), nil
	}

	// Clock skew: documentation/sync-protocol.md §Edge Cases §Clock Skew —
	// a client updated_at more than 24h off from server time is replaced
	// by server time before the LWW comparison and persistence.
	now := s.clock()
	skew := now.Sub(updatedAt)
	if skew < 0 {
		skew = -skew
	}
	if skew > maxClockSkew {
		updatedAt = now
	}

	var deletedAt pgtype.Timestamptz
	if data.Deleted {
		deletedAt = pgtype.Timestamptz{Time: updatedAt, Valid: true}
	}

	upserted, err := s.Queries.UpsertFocusSession(ctx, storedb.UpsertFocusSessionParams{
		ID:               id,
		UserID:           userID,
		DeviceID:         deviceID,
		ProjectID:        projectID,
		Kind:             data.Kind,
		PlannedDurationS: data.PlannedDurationS,
		StartedAt:        pgtype.Timestamptz{Time: startedAt, Valid: true},
		EndedAt:          endedAt,
		Status:           data.Status,
		Note:             data.Note,
		UpdatedAt:        pgtype.Timestamptz{Time: updatedAt, Valid: true},
		DeletedAt:        deletedAt,
	})
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return pushResult{}, fmt.Errorf("upsert focus session: %w", err)
		}

		// Zero rows: either a same-user LWW loss (an older/no-newer write
		// than what's already stored), or the id collides with a
		// different user's row entirely (tenant-isolation violation).
		existing, getErr := s.Queries.GetFocusSessionByID(ctx, id)
		if getErr != nil {
			return pushResult{}, fmt.Errorf("look up focus session after no-op upsert: %w", getErr)
		}
		if existing.UserID != userID {
			return invalidFocusSession(index, data.ID, "FORBIDDEN", "this id belongs to a different user"), nil
		}
		return duplicateFocusSession(index, data.ID), nil
	}

	return appliedFocusSession(index, data.ID, upserted.ServerSeq), nil
}

// resolveApp implements documentation/architecture-backend.md §Ingestion
// Pipeline stage 2 (app catalog resolution): look up the apps row for
// bundleID/platform, creating one if none exists yet.
func (s *Service) resolveApp(ctx context.Context, bundleID, platform string) (storedb.App, error) {
	app, err := s.Queries.GetAppByBundleID(ctx, storedb.GetAppByBundleIDParams{BundleID: bundleID, Platform: platform})
	if err == nil {
		return app, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storedb.App{}, err
	}

	created, err := s.Queries.CreateApp(ctx, storedb.CreateAppParams{BundleID: bundleID, Platform: platform, Name: bundleID})
	if err == nil {
		return created, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storedb.App{}, err
	}

	// CreateApp raced with a concurrent insert of the same bundle_id -
	// platform pair (ON CONFLICT DO NOTHING returned zero rows); the
	// winning row is now visible.
	return s.Queries.GetAppByBundleID(ctx, storedb.GetAppByBundleIDParams{BundleID: bundleID, Platform: platform})
}

// resolveCategory implements documentation/architecture-backend.md
// §Ingestion Pipeline stage 3 (category resolution): a user-specific
// override in user_app_settings, falling back to the app's default
// category.
func (s *Service) resolveCategory(ctx context.Context, userID pgtype.UUID, app storedb.App) (pgtype.UUID, error) {
	setting, err := s.Queries.GetUserAppSettingByUserAndApp(ctx, storedb.GetUserAppSettingByUserAndAppParams{
		UserID: userID,
		AppID:  app.ID,
	})
	if err == nil && setting.CategoryID.Valid {
		return setting.CategoryID, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return pgtype.UUID{}, err
	}
	return app.DefaultCategoryID, nil
}
