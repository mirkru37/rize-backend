package sync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// defaultActivityType is substituted for `type` when a client omits it.
// documentation/sync-protocol.md §Push's worked request example for
// activity_event doesn't include `type`, but the underlying
// activity_events.type column is declared NOT NULL by the deployed schema
// (internal/store/migrations/000011_create_activity_events.up.sql) even
// though database-schema.md's table description only documents its CHECK
// constraint. To keep a doc-conformant client (one that never sends
// `type`) fully functional against that NOT NULL column, an omitted `type`
// defaults to the safest inert value, "manual", rather than guessing at
// automatic-tracking behavior (e.g. "app_active") the client never
// reported.
const defaultActivityType = "manual"

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
	// type is optional on the wire (see dto.go); an omitted value defaults
	// to defaultActivityType, but an explicitly-supplied value must still
	// be one of the documented enum values.
	eventType := defaultActivityType
	if data.Type != nil {
		if !validActivityType[*data.Type] {
			return invalidActivityEvent(index, data.EventID, "VALIDATION_ERROR", "type must be one of app_active, idle, locked, mobile_usage, manual"), nil
		}
		eventType = *data.Type
	}

	app, err := s.resolveApp(ctx, data.AppBundleID, platform)
	if err != nil {
		return pushResult{}, fmt.Errorf("resolve app: %w", err)
	}

	categoryID, err := s.resolveCategory(ctx, userID, app)
	if err != nil {
		return pushResult{}, fmt.Errorf("resolve category: %w", err)
	}

	startedAtTs := pgtype.Timestamptz{Time: startedAt, Valid: true}

	_, err = s.Queries.InsertActivityEvent(ctx, storedb.InsertActivityEventParams{
		EventID:     eventID,
		UserID:      userID,
		DeviceID:    deviceID,
		StartedAt:   startedAtTs,
		EndedAt:     pgtype.Timestamptz{Time: endedAt, Valid: true},
		Type:        eventType,
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
		if !errors.Is(err, pgx.ErrNoRows) {
			// Defense-in-depth per documentation/sync-protocol.md §Push: a
			// constraint violation on this INSERT (e.g. an unresolvable
			// category_id) is a per-item "invalid", never a batch-aborting
			// 500.
			if code, message, ok := constraintViolationResult(err); ok {
				return invalidActivityEvent(index, data.EventID, code, message), nil
			}
			return pushResult{}, fmt.Errorf("insert activity event: %w", err)
		}

		// ON CONFLICT DO NOTHING found an existing row under this
		// idempotency key (user_id, event_id, started_at). Per
		// documentation/sync-protocol.md ("tombstoning an existing event
		// is a subsequent push of the same event_id with deleted: true
		// and the same started_at"), a tombstone push against an
		// already-ingested event must flip that row's deleted flag to
		// true rather than being silently dropped as a no-op duplicate.
		if data.Deleted {
			_, tombErr := s.Queries.TombstoneActivityEvent(ctx, storedb.TombstoneActivityEventParams{
				UserID:    userID,
				EventID:   eventID,
				StartedAt: startedAtTs,
			})
			if tombErr != nil {
				if errors.Is(tombErr, pgx.ErrNoRows) {
					// The row was already tombstoned by a prior push: a
					// no-op re-tombstone, reported as "duplicate".
					return duplicateActivityEvent(index, data.EventID), nil
				}
				return pushResult{}, fmt.Errorf("tombstone activity event: %w", tombErr)
			}
			return appliedActivityEvent(index, data.EventID), nil
		}

		// A non-tombstone replay of the same key: a no-op duplicate, per
		// documentation/sync-protocol.md §Entity Classes.
		return duplicateActivityEvent(index, data.EventID), nil
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
	if projectID.Valid {
		// Tenant-scope project_id per documentation/security.md §Tenant
		// Isolation: existence alone (which the projects_id_fkey FK below
		// would otherwise be the only check for) isn't enough — the project
		// must also belong to the authenticated caller. A project_id that's
		// either unknown or owned by a different user is reported
		// identically ("invalid"/FOREIGN_KEY_VIOLATION) so the response
		// never reveals whether another tenant's project exists.
		if _, getErr := s.Queries.GetProjectByIDForUser(ctx, storedb.GetProjectByIDForUserParams{ID: projectID, UserID: userID}); getErr != nil {
			if errors.Is(getErr, pgx.ErrNoRows) {
				return invalidFocusSession(index, data.ID, "FOREIGN_KEY_VIOLATION", "project_id does not reference an existing project owned by this user"), nil
			}
			return pushResult{}, fmt.Errorf("resolve project: %w", getErr)
		}
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
			// Defense-in-depth: the GetProjectByIDForUser check above
			// should already keep an out-of-tenant/nonexistent project_id
			// from reaching this INSERT, but a constraint violation
			// (e.g. TOCTOU with a concurrently-deleted project) is still a
			// per-item "invalid", never a batch-aborting 500, per
			// documentation/sync-protocol.md §Push.
			if code, message, ok := constraintViolationResult(err); ok {
				return invalidFocusSession(index, data.ID, code, message), nil
			}
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

// constraintViolationResult inspects err for a Postgres constraint-violation
// SQLSTATE and, if found, returns the (code, message) pair for a per-item
// "invalid" pushResult per documentation/sync-protocol.md §Push ("a batch is
// never rejected as a whole because one item is invalid"). ok is false for
// any other error (including no error at all), signaling the caller should
// treat it as an unexpected, batch-aborting failure instead.
//
// Messages are static or built only from pgErr.ConstraintName — never from
// pgErr.Message or the offending payload values, which can echo submitted
// data back to the client.
func constraintViolationResult(err error) (code, message string, ok bool) {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return "", "", false
	}
	switch pgErr.Code {
	case "23503": // foreign_key_violation
		return "FOREIGN_KEY_VIOLATION", "referenced row does not exist", true
	case "23514", "23505", "23502": // check/unique/not_null violation
		message := "value violates a database constraint"
		if pgErr.ConstraintName != "" {
			message = fmt.Sprintf("value violates constraint %q", pgErr.ConstraintName)
		}
		return "VALIDATION_ERROR", message, true
	default:
		return "", "", false
	}
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
